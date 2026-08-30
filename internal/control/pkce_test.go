package control

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewPKCEShape(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	require.NoError(t, err)

	// RFC 7636 要求 verifier 长度在 43-128 之间
	require.GreaterOrEqual(t, len(verifier), 43)
	require.LessOrEqual(t, len(verifier), 128)
	require.NotContains(t, verifier, "=")
	require.NotContains(t, challenge, "=")
}

func TestNewPKCEChallengeIsS256OfVerifier(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	require.NoError(t, err)

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	require.Equal(t, want, challenge, "challenge 必须是 verifier 的 S256 变换")
}

func TestNewPKCEUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		v, _, err := NewPKCE()
		require.NoError(t, err)
		require.False(t, seen[v])
		seen[v] = true
	}
}

func TestNewStateUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, err := NewState()
		require.NoError(t, err)
		require.NotEmpty(t, s)
		require.False(t, seen[s])
		seen[s] = true
	}
}
