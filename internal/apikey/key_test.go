package apikey

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateProducesValidKey(t *testing.T) {
	plain, hash, prefix, err := Generate()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(plain, "ak-"), "明文必须以 ak- 开头，实际 %q", plain)
	require.Len(t, plain, 3+43, "ak- 加 32 字节 base64url 无填充后应为 46 字符")
	require.NoError(t, ValidateFormat(plain))

	require.Len(t, hash, 64, "sha256 十六进制应为 64 字符")
	require.Equal(t, Hash(plain), hash)

	require.Equal(t, plain[:12], prefix, "前缀取明文前 12 字符用于展示")
}

func TestGenerateProducesUniqueKeys(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		plain, _, _, err := Generate()
		require.NoError(t, err)
		require.False(t, seen[plain], "生成了重复密钥: %s", plain)
		seen[plain] = true
	}
}

func TestHashIsStable(t *testing.T) {
	require.Equal(t, Hash("ak-example"), Hash("ak-example"))
	require.NotEqual(t, Hash("ak-example"), Hash("ak-different"))
}

func TestValidateFormat(t *testing.T) {
	valid, _, _, err := Generate()
	require.NoError(t, err)

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"合法密钥", valid, false},
		{"缺少前缀", strings.TrimPrefix(valid, "ak-"), true},
		{"错误前缀", "sk-" + strings.TrimPrefix(valid, "ak-"), true},
		{"长度不足", "ak-tooshort", true},
		{"空串", "", true},
		{"含非法字符", "ak-" + strings.Repeat("!", 43), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormat(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
