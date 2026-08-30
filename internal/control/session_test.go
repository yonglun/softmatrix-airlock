package control

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateSessionTokenShape(t *testing.T) {
	token, hash, err := GenerateSessionToken()
	require.NoError(t, err)

	require.Len(t, token, 43, "32 字节 base64url 无填充后为 43 字符")
	require.NotContains(t, token, "=", "不应有 base64 填充符")
	require.Len(t, hash, 64, "sha256 十六进制为 64 字符")
	require.Equal(t, HashSessionToken(token), hash)
	require.NotEqual(t, token, hash, "存库的必须是哈希而不是 token 本身")
}

func TestGenerateSessionTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		token, _, err := GenerateSessionToken()
		require.NoError(t, err)
		require.False(t, seen[token], "生成了重复 session token")
		seen[token] = true
	}
}

func TestHashSessionTokenStable(t *testing.T) {
	require.Equal(t, HashSessionToken("abc"), HashSessionToken("abc"))
	require.NotEqual(t, HashSessionToken("abc"), HashSessionToken("abd"))
	require.False(t, strings.Contains(HashSessionToken("abc"), "abc"))
}
