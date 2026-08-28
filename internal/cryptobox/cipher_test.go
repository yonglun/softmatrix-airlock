package cryptobox

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	plain := "sk-litellm-upstream-abc123"
	enc, err := c.Encrypt(plain)
	require.NoError(t, err)
	require.NotEqual(t, plain, enc)

	got, err := c.Decrypt(enc)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestEncryptProducesDifferentCiphertextEachTime(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	a, err := c.Encrypt("same-input")
	require.NoError(t, err)
	b, err := c.Encrypt("same-input")
	require.NoError(t, err)

	require.NotEqual(t, a, b, "随机 nonce 应使同一明文每次产出不同密文")
}

func TestNewCipherRejectsWrongKeyLength(t *testing.T) {
	_, err := NewCipher([]byte("too-short"))
	require.Error(t, err)
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	enc, err := c.Encrypt("secret")
	require.NoError(t, err)

	// 直接对解码后的字节翻转最后一位（落在 GCM 认证 tag 内），保证语义上
	// 一定发生了篡改。之前用 "A" + enc[1:] 覆盖首字符，当原始首字节的高 6 位
	// 恰好已经是 0（概率 1/64）时会是一次空操作，导致测试偶发失败。
	raw, err := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)

	_, err = c.Decrypt(tampered)
	require.Error(t, err)
}

func TestDecryptRejectsTooShortInput(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	_, err = c.Decrypt("YWJj") // "abc"，短于 nonce 长度
	require.Error(t, err)
}
