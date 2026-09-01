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
	rpm_limit, tpm_limit, expires_at, created_at`

func scanKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var models []byte
	if err := row.Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.OrgID, &k.UserID, &k.Name,
		&k.UpstreamKeyEnc, &k.Status, &models, &k.MaxBudget, &k.BudgetDuration,
		&k.RPMLimit, &k.TPMLimit, &k.ExpiresAt, &k.CreatedAt); err != nil {
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
