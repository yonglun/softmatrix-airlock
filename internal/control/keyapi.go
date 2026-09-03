package control

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/softmatrix/airlock/internal/authz"
)

type KeyAPI struct {
	issuer   *KeyIssuer
	keys     KeyStore
	orgs     OrgStore
	resolver *authz.Resolver
}

// orgs 供 HandleRevokeOrg 用：按子树吊销要先把节点 ID 换成物化路径。
// resolver 供 HandleRevoke/HandleRotate 用：这两条路径里只有密钥 ID，
// 中间件拿不到它所属的节点，判定只能下沉到处理器自己做。
func NewKeyAPI(issuer *KeyIssuer, keys KeyStore, orgs OrgStore, resolver *authz.Resolver) *KeyAPI {
	return &KeyAPI{issuer: issuer, keys: keys, orgs: orgs, resolver: resolver}
}

// keyView 是密钥的对外表示。刻意不含 UpstreamKeyEnc——
// 上游密钥从不离开控制面，连字段名都不出现在响应里。
type keyView struct {
	ID             string     `json:"id"`
	KeyPrefix      string     `json:"key_prefix"`
	OrgID          string     `json:"org_id"`
	UserID         string     `json:"user_id"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	Models         []string   `json:"models"`
	MaxBudget      *float64   `json:"max_budget"`
	BudgetDuration *string    `json:"budget_duration"`
	RPMLimit       *int       `json:"rpm_limit"`
	TPMLimit       *int       `json:"tpm_limit"`
	ExpiresAt      *time.Time `json:"expires_at"`
	// 轮换状态。共存窗口还剩多久，是轮换这个功能唯一需要被看见的信息；
	// 没有这两个字段，界面只能说「轮换成功」，说不出旧凭据还能用多久。
	RotatedAt        *time.Time `json:"rotated_at"`
	PrevKeyExpiresAt *time.Time `json:"prev_key_expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

func viewOf(k *APIKey) keyView {
	return keyView{
		ID: k.ID, KeyPrefix: k.KeyPrefix, OrgID: k.OrgID, UserID: k.UserID,
		Name: k.Name, Status: k.Status, Models: orEmptyStrings(k.Models),
		MaxBudget: k.MaxBudget, BudgetDuration: k.BudgetDuration,
		RPMLimit: k.RPMLimit, TPMLimit: k.TPMLimit,
		ExpiresAt: k.ExpiresAt,
		RotatedAt: k.RotatedAt, PrevKeyExpiresAt: k.PrevKeyExpiresAt,
		CreatedAt: k.CreatedAt,
	}
}

// HandleIssue 签发一把新密钥。ak- 明文只在这里返回一次。
func (a *KeyAPI) HandleIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID          string     `json:"org_id"`
		UserID         string     `json:"user_id"`
		Name           string     `json:"name"`
		Models         *[]string  `json:"models"`
		MaxBudget      *float64   `json:"max_budget"`
		BudgetDuration *string    `json:"budget_duration"`
		RPMLimit       *int       `json:"rpm_limit"`
		TPMLimit       *int       `json:"tpm_limit"`
		ExpiresAt      *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	// Models 用指针接收，才能区分「显式传了空数组」与「压根没传」。
	// 上游把两者都当成放行全部模型，这个 fail-open 只能在边界堵。
	if body.Models == nil {
		writeError(w, http.StatusBadRequest, "models_required",
			"必须显式提供 models 字段；空数组表示放行全部模型")
		return
	}

	plaintext, k, err := a.issuer.Issue(r.Context(), IssueRequest{
		OrgID: body.OrgID, UserID: body.UserID, Name: body.Name,
		Models: *body.Models, MaxBudget: body.MaxBudget,
		BudgetDuration: body.BudgetDuration,
		RPMLimit:       body.RPMLimit, TPMLimit: body.TPMLimit,
		ExpiresAt: body.ExpiresAt,
	})
	if err != nil {
		writeKeyError(w, err, "签发密钥失败")
		return
	}

	writeJSON(w, http.StatusCreated, struct {
		keyView
		Key string `json:"key"`
	}{keyView: viewOf(k), Key: plaintext})
}

// HandleList 列出某节点下的密钥。
//
// 带 ?subtree=true 时改为列出整个子树，供子树批量吊销的预览用——
// 预览与开火用的是同一套节点选择子句（见 KeyStore.ListByOrgSubtree）。
// 权限不必额外判定：中间件已按 key:read 校验过这个节点，而节点上的
// 授予本就覆盖其子树。
func (a *KeyAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var list []*APIKey
	var err error
	if r.URL.Query().Get("subtree") == "true" {
		org, gerr := a.orgs.Get(r.Context(), id)
		if gerr != nil {
			writeKeyError(w, gerr, "查询密钥失败")
			return
		}
		list, err = a.keys.ListByOrgSubtree(r.Context(), org.Path)
	} else {
		list, err = a.keys.ListByOrg(r.Context(), id)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询密钥失败")
		return
	}
	out := make([]keyView, 0, len(list))
	for _, k := range list {
		out = append(out, viewOf(k))
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleRevoke 吊销一把密钥。
//
// 权限判定下沉到这里：路径里只有密钥 ID，中间件拿不到它所属的节点，
// 与 DELETE /api/grants/{id} 是同一类例外。
func (a *KeyAPI) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	k, err := a.keys.Get(r.Context(), id)
	if err != nil {
		writeKeyError(w, err, "吊销密钥失败")
		return
	}

	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	// 判定与 HandleRotate 对齐：责任人本人，或在该密钥所属节点上
	// 持有 key:write 的管理员。
	//
	// 责任人本人能吊销自己的密钥，是密钥泄露时唯一的自助止血路径：
	// 轮换救不了场（共存窗口内旧凭据仍然有效，且 API 无法表达零窗口）。
	// 代价是责任人手滑能断掉自己在跑的生产——那靠界面的二次确认拦，
	// 而不是靠从他手里收走止血能力。
	if u.ID != k.UserID {
		allowed, err := a.resolver.Can(r.Context(), subjectOf(u), authz.PermKeyWrite, &k.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "permission_denied", "没有吊销该密钥的权限")
			return
		}
	}

	if err := a.issuer.Revoke(r.Context(), id); err != nil {
		writeKeyError(w, err, "吊销密钥失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRotate 换发一把密钥的客户端凭据。新的 ak- 明文只在这里返回一次。
//
// 权限判定下沉到这里：路径里只有密钥 ID，中间件拿不到它所属的节点，
// 与 HandleRevoke 是同一类例外。允许两种人轮换——
// 责任人本人（自助，与 P1.3b 的自助领取一脉相承），
// 或在该密钥所属节点上持有 key:write 的管理员（替人处置）。
func (a *KeyAPI) HandleRotate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// WindowSeconds 是共存窗口秒数；0 或缺省表示用默认值。
		WindowSeconds int64 `json:"window_seconds"`
	}
	// 空请求体是合法的（表示全用默认值），因此解码失败不当错误处理。
	_ = json.NewDecoder(r.Body).Decode(&body)

	id := r.PathValue("id")
	k, err := a.keys.Get(r.Context(), id)
	if err != nil {
		writeKeyError(w, err, "轮换密钥失败")
		return
	}

	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	if u.ID != k.UserID {
		allowed, err := a.resolver.Can(r.Context(), subjectOf(u), authz.PermKeyWrite, &k.OrgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "permission_denied", "没有轮换该密钥的权限")
			return
		}
	}

	plaintext, rotated, err := a.issuer.Rotate(
		r.Context(), id, time.Duration(body.WindowSeconds)*time.Second)
	if err != nil {
		writeKeyError(w, err, "轮换密钥失败")
		return
	}

	writeJSON(w, http.StatusOK, struct {
		keyView
		Key string `json:"key"`
	}{keyView: viewOf(rotated), Key: plaintext})
}

// writeKeyError 把密钥相关的领域错误映射为合适的状态码。
func writeKeyError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
	case errors.Is(err, ErrOrgNotKeyHolder):
		writeError(w, http.StatusConflict, "org_not_key_holder",
			"该节点不是密钥边界，请先将其标记为 key holder")
	case errors.Is(err, ErrAPIKeyNotFound):
		writeError(w, http.StatusNotFound, "key_not_found", "密钥不存在")
	case errors.Is(err, ErrKeyNotActive):
		writeError(w, http.StatusConflict, "key_not_active",
			"密钥不处于可用状态，无法轮换")
	case errors.Is(err, ErrWindowTooLong):
		writeError(w, http.StatusBadRequest, "window_too_long",
			"共存窗口超过上限（最长 30 天）")
	default:
		writeError(w, http.StatusBadGateway, "upstream_failed", fallback)
	}
}

// revokeResult 是两个批量吊销端点的统一响应体。
type revokeResult struct {
	Revoked int64 `json:"revoked"`
}

// HandleRevokeOrg 吊销某节点子树下的全部密钥。
//
// 权限由中间件判定（路径里就是节点 ID，TargetFromPath 能取到）。
// 这里只做本地 UPDATE 就返回：上游没有批量封禁接口，逐把封会超时，
// 交给 BlockPendingUpstream 扫描收敛。此刻安全属性已经满足——
// Edge 每请求查库，下一个请求就拒。
func (a *KeyAPI) HandleRevokeOrg(w http.ResponseWriter, r *http.Request) {
	org, err := a.orgs.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeKeyError(w, err, "吊销密钥失败")
		return
	}

	n, err := a.keys.RevokeByOrgSubtree(r.Context(), org.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "批量吊销失败")
		return
	}
	slog.Warn("按组织子树批量吊销密钥", "org_id", org.ID, "path", org.Path, "revoked", n)
	writeJSON(w, http.StatusOK, revokeResult{Revoked: n})
}

// revokeAllConfirmation 是紧急全局吊销必须原样带上的确认字符串。
// 这是不可逆操作唯一的事前防护，必须精确匹配。
const revokeAllConfirmation = "REVOKE ALL KEYS"

// HandleRevokeAll 紧急吊销全系统密钥。break glass，不可逆。
//
// 权限由中间件判定（全局权限 key:revoke_all）。确认字符串是第二道闸：
// 全局权限管住「谁能按」，确认字符串管住「不会手滑按到」。
func (a *KeyAPI) HandleRevokeAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	if body.Confirm != revokeAllConfirmation {
		writeError(w, http.StatusBadRequest, "confirm_required",
			`紧急全局吊销不可逆，请在请求体中带上 {"confirm":"`+revokeAllConfirmation+`"}`)
		return
	}

	n, err := a.keys.RevokeAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "全局吊销失败")
		return
	}
	slog.Error("已执行紧急全局吊销", "revoked", n)
	writeJSON(w, http.StatusOK, revokeResult{Revoked: n})
}
