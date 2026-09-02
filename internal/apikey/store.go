package apikey

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

var (
	ErrKeyNotFound = errors.New("密钥不存在")
	ErrKeyRevoked  = errors.New("密钥已吊销")
	ErrKeyExpired  = errors.New("密钥已过期")
)

// Key 是一个已解密、可直接使用的 Airlock 虚拟密钥。
type Key struct {
	ID          string
	Prefix      string
	OrgID       string
	UserID      string
	UpstreamKey string // 已解密的上游 LiteLLM 密钥
	Status      string
	ExpiresAt   *time.Time
	// ViaPrevKey 表示这次是用轮换前的旧凭据进来的。供可观测性使用：
	// 客户端若到窗口结束都没换上新凭据，到期瞬间会集体 401。
	ViaPrevKey bool
	// PrevKeyExpiresAt 是旧凭据的停用时刻，仅在 ViaPrevKey 为真时有意义。
	PrevKeyExpiresAt *time.Time
}

// Validate 检查密钥在 now 时刻是否可用。
func (k *Key) Validate(now time.Time) error {
	if k.Status != StatusActive {
		return ErrKeyRevoked
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return ErrKeyExpired
	}
	return nil
}

// Store 按哈希查询密钥。
type Store interface {
	ByHash(ctx context.Context, hash string) (*Key, error)
}

// MemoryStore 是测试与本地开发用的内存实现。
type MemoryStore struct {
	mu   sync.RWMutex
	keys map[string]*Key
}

func NewMemoryStore(keys map[string]*Key) *MemoryStore {
	if keys == nil {
		keys = make(map[string]*Key)
	}
	return &MemoryStore{keys: keys}
}

func (s *MemoryStore) ByHash(_ context.Context, hash string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[hash]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return k, nil
}

// Put 供测试与本地开发写入密钥。
func (s *MemoryStore) Put(hash string, k *Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[hash] = k
}
