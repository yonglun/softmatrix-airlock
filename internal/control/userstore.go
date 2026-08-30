package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type postgresUserStore struct {
	db *sql.DB
}

func NewPostgresUserStore(db *sql.DB) UserStore {
	return &postgresUserStore{db: db}
}

const userColumns = `id, external_id, email, display_name, status,
	is_platform_admin, primary_org_id, last_login_at, reconciled_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.ExternalID, &u.Email, &u.DisplayName, &u.Status,
		&u.IsPlatformAdmin, &u.PrimaryOrgID, &u.LastLoginAt, &u.ReconciledAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ByID 按 Airlock 内部 ID 查用户，不过滤状态——
// 会话中间件需要能查到已禁用的用户，才能返回 403 而不是含糊的 401。
func (s *postgresUserStore) ByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("按 id 查用户失败: %w", err)
	}
	return u, nil
}

func (s *postgresUserStore) ByExternalID(ctx context.Context, externalID string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE external_id = $1`, externalID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("按 external_id 查用户失败: %w", err)
	}
	return u, nil
}

// Upsert 按 external_id 插入或更新。
// 只刷新 IdP 侧权威的画像字段（email/display_name）与 last_login_at；
// is_platform_admin、primary_org_id、status 是 Airlock 自己管理的，不被登录覆盖。
func (s *postgresUserStore) Upsert(ctx context.Context, u *User) (*User, error) {
	id := u.ID
	if id == "" {
		id = uuid.NewString()
	}
	status := u.Status
	if status == "" {
		status = UserStatusActive
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO users (id, external_id, email, display_name, status, last_login_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (external_id) DO UPDATE SET
			email         = EXCLUDED.email,
			display_name  = EXCLUDED.display_name,
			last_login_at = COALESCE(EXCLUDED.last_login_at, users.last_login_at)
		RETURNING `+userColumns,
		id, u.ExternalID, u.Email, u.DisplayName, status, u.LastLoginAt)

	got, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("upsert 用户失败: %w", err)
	}
	return got, nil
}

func (s *postgresUserStore) ListActive(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE status = $1 ORDER BY created_at`,
		UserStatusActive)
	if err != nil {
		return nil, fmt.Errorf("查询活跃用户失败: %w", err)
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描用户行失败: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *postgresUserStore) MarkDisabled(ctx context.Context, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE users SET status = $1, reconciled_at = $2
		WHERE id = ANY($3)`,
		UserStatusDisabled, time.Now().UTC(), userIDs)
	if err != nil {
		return fmt.Errorf("标记用户禁用失败: %w", err)
	}
	return nil
}

func (s *postgresUserStore) CountPlatformAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE is_platform_admin = true AND status = $1`,
		UserStatusActive).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计平台管理员失败: %w", err)
	}
	return n, nil
}

func (s *postgresUserStore) SetPlatformAdmin(ctx context.Context, userID string, v bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET is_platform_admin = $1 WHERE id = $2`, v, userID)
	if err != nil {
		return fmt.Errorf("设置平台管理员失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}
