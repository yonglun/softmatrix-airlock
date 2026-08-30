package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type postgresSessionStore struct {
	db *sql.DB
}

func NewPostgresSessionStore(db *sql.DB) SessionStore {
	return &postgresSessionStore{db: db}
}

func (s *postgresSessionStore) Create(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, expires_at, last_seen_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.ID, sess.UserID, sess.ExpiresAt, sess.LastSeenAt, sess.IP, sess.UserAgent)
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	return nil
}

// Get 只返回未过期的会话——过期判断下沉到 SQL，
// 调用方拿到的会话一定可用，不需要各自再判一次。
func (s *postgresSessionStore) Get(ctx context.Context, id string) (*Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, expires_at, last_seen_at, ip, user_agent
		FROM sessions WHERE id = $1 AND expires_at > now()`, id).
		Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.LastSeenAt, &sess.IP, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询会话失败: %w", err)
	}
	return &sess, nil
}

func (s *postgresSessionStore) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = $1 WHERE id = $2`, at, id)
	if err != nil {
		return fmt.Errorf("更新会话活跃时间失败: %w", err)
	}
	return nil
}

func (s *postgresSessionStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("删除会话失败: %w", err)
	}
	return nil
}

func (s *postgresSessionStore) DeleteByUser(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("按用户删除会话失败: %w", err)
	}
	return res.RowsAffected()
}

func (s *postgresSessionStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, before)
	if err != nil {
		return 0, fmt.Errorf("清理过期会话失败: %w", err)
	}
	return res.RowsAffected()
}

type postgresLoginStateStore struct {
	db *sql.DB
}

func NewPostgresLoginStateStore(db *sql.DB) LoginStateStore {
	return &postgresLoginStateStore{db: db}
}

func (s *postgresLoginStateStore) Create(ctx context.Context, ls LoginState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO login_states (id, state, pkce_verifier, redirect_to, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		ls.ID, ls.State, ls.PKCEVerifier, ls.RedirectTo, ls.ExpiresAt)
	if err != nil {
		return fmt.Errorf("创建登录状态失败: %w", err)
	}
	return nil
}

// Take 用 DELETE ... RETURNING 在一条语句里完成「取出并作废」，
// 天然原子——并发重放同一个 id 只有一个能拿到。
func (s *postgresLoginStateStore) Take(ctx context.Context, id string) (*LoginState, error) {
	var ls LoginState
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM login_states
		WHERE id = $1 AND expires_at > now()
		RETURNING id, state, pkce_verifier, redirect_to, expires_at`, id).
		Scan(&ls.ID, &ls.State, &ls.PKCEVerifier, &ls.RedirectTo, &ls.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLoginStateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("取用登录状态失败: %w", err)
	}
	return &ls, nil
}

func (s *postgresLoginStateStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM login_states WHERE expires_at <= $1`, before)
	if err != nil {
		return 0, fmt.Errorf("清理过期登录状态失败: %w", err)
	}
	return res.RowsAffected()
}
