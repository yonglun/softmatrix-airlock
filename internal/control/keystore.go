package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type postgresKeyStore struct {
	db *sql.DB
}

func NewPostgresKeyStore(db *sql.DB) KeyStore {
	return &postgresKeyStore{db: db}
}

const keyColumns = `id, key_hash, key_prefix, org_id, user_id, name,
	upstream_key_enc, status, models, max_budget, budget_duration,
	rpm_limit, tpm_limit, expires_at,
	prev_key_hash, prev_key_expires_at, rotated_at,
	upstream_blocked_at, upstream_block_attempts, created_at`

func scanKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var models []byte
	if err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.OrgID, &k.UserID, &k.Name,
		&k.UpstreamKeyEnc, &k.Status, &models, &k.MaxBudget, &k.BudgetDuration,
		&k.RPMLimit, &k.TPMLimit, &k.ExpiresAt,
		&k.PrevKeyHash, &k.PrevKeyExpiresAt, &k.RotatedAt,
		&k.UpstreamBlockedAt, &k.UpstreamBlockAttempts, &k.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(models, &k.Models); err != nil {
		return nil, fmt.Errorf("解析 models 失败: %w", err)
	}
	return &k, nil
}

func (s *postgresKeyStore) CreatePending(ctx context.Context, k *APIKey) error {
	models, err := json.Marshal(orEmptyStrings(k.Models))
	if err != nil {
		return fmt.Errorf("序列化 models 失败: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO api_keys
			(id, key_hash, key_prefix, org_id, user_id, name, upstream_key_enc,
			 status, models, max_budget, budget_duration, rpm_limit, tpm_limit, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8,$9,$10,$11,$12,$13)`,
		k.ID, k.KeyHash, k.KeyPrefix, k.OrgID, k.UserID, k.Name, k.UpstreamKeyEnc,
		models, k.MaxBudget, k.BudgetDuration, k.RPMLimit, k.TPMLimit, k.ExpiresAt)
	if err != nil {
		return fmt.Errorf("写入待建密钥失败: %w", err)
	}
	return nil
}

func (s *postgresKeyStore) MarkActive(ctx context.Context, id string) error {
	return s.setStatus(ctx, id, "active")
}

func (s *postgresKeyStore) Revoke(ctx context.Context, id string) error {
	return s.setStatus(ctx, id, "revoked")
}

func (s *postgresKeyStore) setStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("更新密钥状态失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

func (s *postgresKeyStore) Get(ctx context.Context, id string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE id = $1`, id)
	k, err := scanKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询密钥失败: %w", err)
	}
	return k, nil
}

func (s *postgresKeyStore) ListByOrg(ctx context.Context, orgID string) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("查询节点密钥失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectKeys(rows)
}

func (s *postgresKeyStore) ListStalePending(ctx context.Context, olderThan time.Duration) ([]*APIKey, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+keyColumns+` FROM api_keys
		 WHERE status = 'pending' AND created_at < $1 ORDER BY created_at`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("查询滞留密钥失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectKeys(rows)
}

func (s *postgresKeyStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除密钥失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

func collectKeys(rows *sql.Rows) ([]*APIKey, error) {
	out := []*APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// orEmptyStrings 把 nil 归一成空切片，保证入库的 models 永远是 JSON 数组而非 null。
func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Rotate 换发客户端凭据。
//
// WHERE status = 'active' 是乐观并发守卫，与 P1.3b 的 Decide/MarkExecuted
// 同一套：已吊销或仍 pending 的密钥不能轮换。
//
// 行里只有一个 prev_key_hash 的位置，因此窗口内再次轮换会让上一次轮出来的
// 成为新的 prev、最初那把当场失效——「最多只保留一代宽限」。这是刻意的：
// 会连着轮两次通常意味着第一次轮出来的也泄漏了。
func (s *postgresKeyStore) Rotate(
	ctx context.Context, id, newHash, newPrefix string, prevExpiresAt time.Time,
) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_keys SET
			key_hash = $1, key_prefix = $2,
			prev_key_hash = key_hash, prev_key_expires_at = $3,
			rotated_at = $4
		WHERE id = $5 AND status = 'active'`,
		newHash, newPrefix, prevExpiresAt.UTC(), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("轮换密钥失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// 分辨「不存在」与「状态不对」，让调用方能返回正确的状态码。
		if _, gerr := s.Get(ctx, id); gerr != nil {
			return gerr
		}
		return ErrKeyNotActive
	}
	return nil
}

func (s *postgresKeyStore) RetireExpiredPrevKeys(
	ctx context.Context, now time.Time,
) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_keys SET prev_key_hash = NULL, prev_key_expires_at = NULL
		WHERE prev_key_hash IS NOT NULL AND prev_key_expires_at <= $1`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("清理过期旧凭据失败: %w", err)
	}
	return res.RowsAffected()
}

// RevokeByOrgSubtree 按物化路径前缀吊销整棵子树。
//
// 必须加分隔符再比前缀：/root/rd 是 /root/rd2 的前缀，但 rd2 并不在 rd 的
// 子树里。这个陷阱在 P1.2b 的权限判定与 P1.3b 的审批人查找里各踩过一次。
//
// pending 也要覆盖：那些密钥上游可能已经建成。
func (s *postgresKeyStore) RevokeByOrgSubtree(
	ctx context.Context, orgPath string,
) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_keys SET status = 'revoked'
		WHERE status IN ('active','pending')
		  AND org_id IN (
		      SELECT id FROM organizations
		      WHERE path = $1 OR path LIKE $1 || '/%'
		  )`, orgPath)
	if err != nil {
		return 0, fmt.Errorf("按子树吊销密钥失败: %w", err)
	}
	return res.RowsAffected()
}

// ListByOrgSubtree 返回子树下的全部密钥，不限状态。
//
// 节点选择子句与 RevokeByOrgSubtree 逐字相同，这就是它存在的理由：
// 子树批量吊销不可逆，预览的唯一价值是让人看见即将发生什么。
// 两处匹配规则一旦分叉，预览比没有预览更危险。
func (s *postgresKeyStore) ListByOrgSubtree(
	ctx context.Context, orgPath string,
) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+keyColumns+` FROM api_keys
		WHERE org_id IN (
		    SELECT id FROM organizations
		    WHERE path = $1 OR path LIKE $1 || '/%'
		)
		ORDER BY created_at DESC`, orgPath)
	if err != nil {
		return nil, fmt.Errorf("查询子树密钥失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectKeys(rows)
}

// RevokeAll 是 break glass：吊销全系统密钥。不可逆。
func (s *postgresKeyStore) RevokeAll(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET status = 'revoked' WHERE status IN ('active','pending')`)
	if err != nil {
		return 0, fmt.Errorf("全局吊销密钥失败: %w", err)
	}
	return res.RowsAffected()
}

func (s *postgresKeyStore) ListRevokedUnblocked(
	ctx context.Context, maxAttempts, limit int,
) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+keyColumns+` FROM api_keys
		WHERE status = 'revoked' AND upstream_blocked_at IS NULL
		  AND upstream_block_attempts < $1
		ORDER BY created_at LIMIT $2`, maxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待封禁密钥失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectKeys(rows)
}

func (s *postgresKeyStore) MarkUpstreamBlocked(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET upstream_blocked_at = $1 WHERE id = $2`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("标记上游已封禁失败: %w", err)
	}
	return nil
}

func (s *postgresKeyStore) RecordBlockAttempt(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET upstream_block_attempts = upstream_block_attempts + 1
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("记录封禁尝试失败: %w", err)
	}
	return nil
}
