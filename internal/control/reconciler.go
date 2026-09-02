package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/softmatrix/airlock/internal/cryptobox"
)

// IdentitySource 提供 IdP 侧的用户启用状态。
// 返回 external_id -> 是否启用；IdP 里已删除的用户不出现在结果里。
type IdentitySource interface {
	ActiveExternalIDs(ctx context.Context) (map[string]bool, error)
}

// KeyRevoker 吊销指定用户名下的全部密钥。
type KeyRevoker interface {
	RevokeByUsers(ctx context.Context, userIDs []string) (int64, error)
}

type ReconcilerDeps struct {
	Users    UserStore
	Sessions SessionStore
	Identity IdentitySource
	Keys     KeyRevoker
}

type Reconciler struct {
	deps ReconcilerDeps
}

func NewReconciler(deps ReconcilerDeps) *Reconciler {
	return &Reconciler{deps: deps}
}

// ReconcileResult 汇总一轮对账的结果。
type ReconcileResult struct {
	Checked      int
	Disabled     int
	KeysRevoked  int64
	SessionsGone int64
}

// ReconcileOnce 跑一轮对账：把 IdP 侧已离职/禁用的人，在 Airlock 侧
// 标记禁用、吊销其全部密钥、清掉其全部会话。
func (r *Reconciler) ReconcileOnce(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult

	remote, err := r.deps.Identity.ActiveExternalIDs(ctx)
	if err != nil {
		return res, fmt.Errorf("拉取 IdP 用户状态失败: %w", err)
	}
	// 空结果几乎一定是故障（权限问题、查询写错、连错库），
	// 而不是「全公司都离职了」。当作异常中止，避免误伤全员。
	if len(remote) == 0 {
		return res, errors.New("IdP 返回空的用户集合，判定为异常，本轮对账中止")
	}

	local, err := r.deps.Users.ListActive(ctx)
	if err != nil {
		return res, fmt.Errorf("查询本地活跃用户失败: %w", err)
	}
	res.Checked = len(local)

	var toDisable []string
	for _, u := range local {
		if enabled, ok := remote[u.ExternalID]; !ok || !enabled {
			toDisable = append(toDisable, u.ID)
			slog.Info("检测到 IdP 侧已离职或禁用",
				"user_id", u.ID, "email", u.Email, "external_id", u.ExternalID)
		}
	}
	if len(toDisable) == 0 {
		return res, nil
	}

	if err := r.deps.Users.MarkDisabled(ctx, toDisable); err != nil {
		return res, fmt.Errorf("标记用户禁用失败: %w", err)
	}
	res.Disabled = len(toDisable)

	revoked, err := r.deps.Keys.RevokeByUsers(ctx, toDisable)
	if err != nil {
		return res, fmt.Errorf("吊销离职用户密钥失败: %w", err)
	}
	res.KeysRevoked = revoked

	for _, uid := range toDisable {
		n, err := r.deps.Sessions.DeleteByUser(ctx, uid)
		if err != nil {
			return res, fmt.Errorf("清除离职用户会话失败: %w", err)
		}
		res.SessionsGone += n
	}

	slog.Warn("离职对账完成",
		"disabled", res.Disabled, "keys_revoked", res.KeysRevoked,
		"sessions_removed", res.SessionsGone)
	return res, nil
}

// Run 按 interval 周期跑对账，直到 ctx 被取消。
// 单轮失败只记日志不退出——IdP 短暂不可用不该让对账循环整个停掉。
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			slog.Error("离职对账失败，将在下个周期重试", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PostgresKeyRevoker 在 api_keys 表上实现 KeyRevoker，并同步封禁上游。
//
// 与 KeyIssuer.Revoke 保持同样的纵深：本地标记 + 上游 block。
// 上游密钥从不离开控制面，所以少了上游那层不是活的漏洞，
// 但离职场景恰恰是最需要「凭据万一泄漏也用不了」这层兜底的场景。
type PostgresKeyRevoker struct {
	db     *sql.DB
	admin  LiteLLMKeyAdmin
	cipher *cryptobox.Cipher
}

func NewPostgresKeyRevoker(
	db *sql.DB, admin LiteLLMKeyAdmin, cipher *cryptobox.Cipher,
) *PostgresKeyRevoker {
	return &PostgresKeyRevoker{db: db, admin: admin, cipher: cipher}
}

func (k *PostgresKeyRevoker) RevokeByUsers(ctx context.Context, userIDs []string) (int64, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}

	// 先把要吊销的密钥连同上游密文查出来——UPDATE 之后就分不清
	// 哪些是本次吊销的了。pending 也要算上：那些密钥上游可能已经建成。
	rows, err := k.db.QueryContext(ctx, `
		SELECT id, upstream_key_enc FROM api_keys
		WHERE user_id = ANY($1) AND status IN ('active','pending')`, userIDs)
	if err != nil {
		return 0, fmt.Errorf("查询待吊销密钥失败: %w", err)
	}
	type target struct{ id, enc string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.enc); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("扫描待吊销密钥失败: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	res, err := k.db.ExecContext(ctx, `
		UPDATE api_keys SET status = 'revoked'
		WHERE user_id = ANY($1) AND status IN ('active','pending')`, userIDs)
	if err != nil {
		return 0, fmt.Errorf("批量吊销密钥失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	// 上游封禁尽力而为。成功即盖戳；失败留白，交给 BlockPendingUpstream
	// 扫描收敛——本地已标记 revoked，Edge 下一个请求就会拒绝该密钥。
	for _, t := range targets {
		upstreamKey, derr := k.cipher.Decrypt(t.enc)
		if derr != nil {
			slog.Warn("解密上游密钥失败，跳过上游封禁", "key_id", t.id, "err", derr)
			continue
		}
		if berr := k.admin.BlockKey(ctx, upstreamKey); berr != nil {
			slog.Warn("封禁上游密钥失败，已留给兜底扫描重试", "key_id", t.id, "err", berr)
			continue
		}
		if _, uerr := k.db.ExecContext(ctx,
			`UPDATE api_keys SET upstream_blocked_at = $1 WHERE id = $2`,
			time.Now().UTC(), t.id); uerr != nil {
			slog.Warn("标记上游已封禁失败，兜底扫描会再来一次",
				"key_id", t.id, "err", uerr)
		}
	}
	return n, nil
}
