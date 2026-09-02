package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/litellm"
)

type KeyIssuerDeps struct {
	Keys   KeyStore
	Orgs   OrgStore
	Admin  LiteLLMKeyAdmin
	Cipher *cryptobox.Cipher
}

type KeyIssuer struct {
	deps KeyIssuerDeps
}

func NewKeyIssuer(deps KeyIssuerDeps) *KeyIssuer {
	return &KeyIssuer{deps: deps}
}

// IssueRequest 是一次签发请求。Models 为空切片表示放行全部模型；
// 「调用方是否显式传了这个字段」由 HTTP 层负责校验，不在这里判断。
type IssueRequest struct {
	OrgID          string
	UserID         string
	Name           string
	Models         []string
	MaxBudget      *float64
	BudgetDuration *string
	RPMLimit       *int
	TPMLimit       *int
	ExpiresAt      *time.Time
}

const upstreamKeyRandomBytes = 32

const (
	// defaultRotationWindow 是共存窗口的默认时长。
	defaultRotationWindow = 24 * time.Hour
	// maxRotationWindow 是上限。不设上限的话，填个 10 年就等于永不轮换。
	maxRotationWindow = 30 * 24 * time.Hour
)

// newUpstreamKey 生成一把我们自己决定值的上游密钥。
//
// 自带值是整个签发设计幂等的基础：崩溃后重试能用同一个值再调一次，
// 上游对重复创建返回 200 且不产生重复实体。
func newUpstreamKey() (string, error) {
	buf := make([]byte, upstreamKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成上游密钥失败: %w", err)
	}
	return "sk-airlock-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Issue 签发一把新密钥，返回 ak- 明文（仅此一次）与落库后的记录。
//
// 顺序是刻意的：先写 pending 本地行，再调上游。反过来的话，
// 「上游建成功但本地没记上」会留下一把能用、却在 Airlock 里看不见
// 因而无法吊销的凭据。
func (i *KeyIssuer) Issue(ctx context.Context, req IssueRequest) (string, *APIKey, error) {
	org, err := i.deps.Orgs.Get(ctx, req.OrgID)
	if err != nil {
		return "", nil, err
	}
	// 上游不校验 team 存在性——挂到不存在的 team 上会静默成功并
	// 失去团队级约束，因此这道闸门只能由我们自己把。
	if !org.IsKeyHolder {
		return "", nil, ErrOrgNotKeyHolder
	}

	plaintext, hash, prefix, err := apikey.Generate()
	if err != nil {
		return "", nil, err
	}
	upstreamKey, err := newUpstreamKey()
	if err != nil {
		return "", nil, err
	}
	enc, err := i.deps.Cipher.Encrypt(upstreamKey)
	if err != nil {
		return "", nil, fmt.Errorf("加密上游密钥失败: %w", err)
	}

	k := &APIKey{
		ID: uuid.NewString(), KeyHash: hash, KeyPrefix: prefix,
		OrgID: req.OrgID, UserID: req.UserID, Name: req.Name,
		UpstreamKeyEnc: enc, Status: "pending", Models: req.Models,
		MaxBudget: req.MaxBudget, BudgetDuration: req.BudgetDuration,
		RPMLimit: req.RPMLimit, TPMLimit: req.TPMLimit, ExpiresAt: req.ExpiresAt,
	}
	if err := i.deps.Keys.CreatePending(ctx, k); err != nil {
		return "", nil, err
	}

	if err := i.deps.Admin.GenerateKey(ctx, litellm.Key{
		Key: upstreamKey, KeyAlias: k.ID, TeamID: &req.OrgID,
		Models: req.Models, MaxBudget: req.MaxBudget,
		BudgetDuration: req.BudgetDuration,
		RPMLimit:       req.RPMLimit, TPMLimit: req.TPMLimit,
		Duration: relativeDuration(req.ExpiresAt, time.Now()),
	}); err != nil {
		i.cleanup(ctx, k.ID, upstreamKey)
		return "", nil, fmt.Errorf("上游签发失败: %w", err)
	}

	if err := i.deps.Keys.MarkActive(ctx, k.ID); err != nil {
		i.cleanup(ctx, k.ID, upstreamKey)
		return "", nil, err
	}
	k.Status = "active"
	return plaintext, k, nil
}

// relativeDuration 把绝对过期时间换算成上游认识的相对秒数。
// 上游会静默丢弃绝对时间 expires，只认 duration。
func relativeDuration(expiresAt *time.Time, now time.Time) *string {
	if expiresAt == nil {
		return nil
	}
	secs := int(expiresAt.Sub(now).Seconds())
	if secs < 1 {
		secs = 1
	}
	d := fmt.Sprintf("%ds", secs)
	return &d
}

// cleanup 清理一次失败的签发。
//
// 顺序不能颠倒：必须先删上游、再删本地行。反过来的话，
// 上游若确实建成了，本地行一删就正好制造出无主凭据。
func (i *KeyIssuer) cleanup(ctx context.Context, id, upstreamKey string) {
	exists, err := i.deps.Admin.KeyExists(ctx, upstreamKey)
	if err != nil {
		// 查不清上游状态时保留本地 pending 行——
		// 留一条看得见的记录，胜过删掉它并可能留下无主凭据。
		slog.Warn("无法确认上游密钥状态，保留 pending 行待下轮清理",
			"key_id", id, "err", err)
		return
	}
	if exists {
		if err := i.deps.Admin.DeleteKey(ctx, upstreamKey); err != nil {
			slog.Warn("删除上游密钥失败，保留 pending 行待下轮清理",
				"key_id", id, "err", err)
			return
		}
	}
	if err := i.deps.Keys.Delete(ctx, id); err != nil {
		slog.Warn("删除待建密钥行失败", "key_id", id, "err", err)
	}
}

// Revoke 吊销一把密钥。
//
// 本地先行：Edge 读的是本地状态，改完下一个请求就会被拒。
// 上游 block 是凭据泄漏时的第二道防线，尽力而为——
// 它失败不该让吊销整体失败，否则上游抖动期间就吊销不掉任何密钥。
func (i *KeyIssuer) Revoke(ctx context.Context, id string) error {
	k, err := i.deps.Keys.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := i.deps.Keys.Revoke(ctx, id); err != nil {
		return err
	}

	upstreamKey, err := i.deps.Cipher.Decrypt(k.UpstreamKeyEnc)
	if err != nil {
		slog.Warn("解密上游密钥失败，跳过上游封禁", "key_id", id, "err", err)
		return nil
	}
	if err := i.deps.Admin.BlockKey(ctx, upstreamKey); err != nil {
		slog.Warn("封禁上游密钥失败，本地已吊销、Edge 已拒绝该密钥",
			"key_id", id, "err", err)
	}
	return nil
}

// CleanupStalePending 清理滞留的 pending 密钥，返回清理条数。
//
// 这些是「进程在上游调用与 MarkActive 之间崩掉」留下的残骸。
// 用户当时已经收到错误、并不知道这把密钥存在，所以是清理而不是补成 active——
// 补成 active 等于凭空多出一把没人知道却能用的密钥。
func (i *KeyIssuer) CleanupStalePending(ctx context.Context, olderThan time.Duration) (int, error) {
	stale, err := i.deps.Keys.ListStalePending(ctx, olderThan)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range stale {
		upstreamKey, err := i.deps.Cipher.Decrypt(k.UpstreamKeyEnc)
		if err != nil {
			slog.Warn("解密待建密钥失败，跳过", "key_id", k.ID, "err", err)
			continue
		}
		before := k.ID
		i.cleanup(ctx, k.ID, upstreamKey)
		if _, err := i.deps.Keys.Get(ctx, before); err != nil {
			n++ // 行确实被清掉了才算数
		}
	}
	return n, nil
}

// Rotate 换发客户端凭据，返回新的 ak- 明文（仅此一次）与轮换后的记录。
//
// 不调用上游：ak- 到 sk- 的映射完全由控制面持有，换掉客户端那一端
// 根本不需要惊动 LiteLLM。因此轮换不可能因为上游宕机而失败——这是
// P1.3a「本地记录优先」推到尽头的形态。
//
// 上游密钥保持不变是刻意的（设计文档 D2）：它加密存放、从不离开控制面，
// 而 ak- 散落在客户端的 .env、CI secrets 与几十份配置里，需要被限定
// 泄漏后可用时长的是后者。保持同一把上游密钥也让预算桶保持连续。
func (i *KeyIssuer) Rotate(
	ctx context.Context, id string, window time.Duration,
) (string, *APIKey, error) {
	if window == 0 {
		window = defaultRotationWindow
	}
	if window < 0 || window > maxRotationWindow {
		return "", nil, ErrWindowTooLong
	}

	plaintext, hash, prefix, err := apikey.Generate()
	if err != nil {
		return "", nil, err
	}
	if err := i.deps.Keys.Rotate(ctx, id, hash, prefix, time.Now().Add(window)); err != nil {
		return "", nil, err
	}

	k, err := i.deps.Keys.Get(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return plaintext, k, nil
}
