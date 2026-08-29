package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// sessionTokenBytes 是 session token 的随机字节数。
const sessionTokenBytes = 32

// GenerateSessionToken 生成一个新的 session token，返回原始 token 与其 sha256 哈希。
// 原始 token 只下发到浏览器 cookie，数据库里只存哈希——
// 与 api_keys 同一原则：数据库泄露不等于会话被劫持。
func GenerateSessionToken() (token, hash string, err error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("读取随机数失败: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashSessionToken(token), nil
}

// HashSessionToken 返回 token 的 sha256 十六进制摘要。
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
