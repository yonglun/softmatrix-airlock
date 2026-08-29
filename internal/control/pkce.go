package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// pkceVerifierBytes 取 32 字节，base64url 后为 43 字符，
// 正好落在 RFC 7636 要求的 43-128 区间下界。
const pkceVerifierBytes = 32

// NewPKCE 生成一对 PKCE verifier 与 S256 challenge。
// verifier 留在服务端（login_states 表），challenge 发给 IdP；
// 回调时用 verifier 换 token，第三方即使截获授权码也无法兑换。
func NewPKCE() (verifier, challenge string, err error) {
	buf := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("生成 PKCE verifier 失败: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)

	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// NewState 生成 OAuth2 的 state 参数，用于防 CSRF。
func NewState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 state 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
