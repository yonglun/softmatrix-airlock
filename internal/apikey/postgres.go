package apikey

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/softmatrix/airlock/internal/cryptobox"
)

// PostgresStore 是 Edge 使用的密钥读取实现。
//
// 每个请求查一次库，不做缓存：吊销因此立即生效，
// 也不需要任何 control→edge 的失效通知通道。
// 按 key_hash 唯一索引查一行约 1ms，相对 LLM 调用本身可忽略。
type PostgresStore struct {
	db     *sql.DB
	cipher *cryptobox.Cipher
}

func NewPostgresStore(db *sql.DB, cipher *cryptobox.Cipher) *PostgresStore {
	return &PostgresStore{db: db, cipher: cipher}
}

func (s *PostgresStore) ByHash(ctx context.Context, hash string) (*Key, error) {
	var (
		k   Key
		enc string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, key_prefix, org_id, user_id, upstream_key_enc, status, expires_at
		FROM api_keys WHERE key_hash = $1`, hash).
		Scan(&k.ID, &k.Prefix, &k.OrgID, &k.UserID, &enc, &k.Status, &k.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询密钥失败: %w", err)
	}

	upstream, err := s.cipher.Decrypt(enc)
	if err != nil {
		return nil, fmt.Errorf("解密上游密钥失败: %w", err)
	}
	k.UpstreamKey = upstream
	return &k, nil
}
