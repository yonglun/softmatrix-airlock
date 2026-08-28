package apikey

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryStoreReturnsActiveKey(t *testing.T) {
	k := &Key{ID: "k1", Prefix: "ak-abcdefgh", OrgID: "o1", UserID: "u1",
		UpstreamKey: "sk-up", Status: StatusActive}
	s := NewMemoryStore(map[string]*Key{"hash1": k})

	got, err := s.ByHash(context.Background(), "hash1")
	require.NoError(t, err)
	require.Equal(t, "k1", got.ID)
	require.Equal(t, "sk-up", got.UpstreamKey)
}

func TestMemoryStoreUnknownHash(t *testing.T) {
	s := NewMemoryStore(nil)
	_, err := s.ByHash(context.Background(), "nope")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestValidateRejectsRevokedKey(t *testing.T) {
	k := &Key{ID: "k1", Status: StatusRevoked}
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyRevoked)
}

func TestValidateRejectsExpiredKey(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	k := &Key{ID: "k1", Status: StatusActive, ExpiresAt: &past}
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyExpired)
}

func TestValidateAcceptsUnexpiredActiveKey(t *testing.T) {
	future := time.Now().Add(time.Hour)
	k := &Key{ID: "k1", Status: StatusActive, ExpiresAt: &future}
	require.NoError(t, k.Validate(time.Now()))
}

func TestValidateAcceptsKeyWithoutExpiry(t *testing.T) {
	k := &Key{ID: "k1", Status: StatusActive}
	require.NoError(t, k.Validate(time.Now()))
}
