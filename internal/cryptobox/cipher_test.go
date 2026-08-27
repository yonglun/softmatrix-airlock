package cryptobox

import (
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

	tampered := "A" + enc[1:]
	_, err = c.Decrypt(tampered)
	require.Error(t, err)
}

func TestDecryptRejectsTooShortInput(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	_, err = c.Decrypt("YWJj") // "abc"，短于 nonce 长度
	require.Error(t, err)
}
