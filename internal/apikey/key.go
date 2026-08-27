// Package apikey 负责 Airlock 自有虚拟密钥（ak- 前缀）的生成、哈希与格式校验。
// 明文只在签发时返回一次，数据库只存 sha256 哈希。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// Prefix 是所有 Airlock 密钥的固定前缀。
	Prefix = "ak-"
	// randomBytes 是密钥随机部分的字节数。
	randomBytes = 32
	// bodyLen 是 32 字节经 base64url 无填充编码后的长度。
	bodyLen = 43
	// PrefixDisplayLen 是存库用于展示的前缀长度（含 "ak-"）。
	PrefixDisplayLen = 12
)

var ErrMalformed = errors.New("密钥格式非法")

// Generate 生成一个新密钥，返回明文、sha256 哈希与展示前缀。
func Generate() (plaintext, hash, prefix string, err error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("读取随机数失败: %w", err)
	}
	plaintext = Prefix + base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, Hash(plaintext), plaintext[:PrefixDisplayLen], nil
}

// Hash 返回密钥明文的 sha256 十六进制摘要。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// ValidateFormat 在查库之前先做廉价的格式校验，挡掉明显非法的输入。
func ValidateFormat(plaintext string) error {
	if !strings.HasPrefix(plaintext, Prefix) {
		return fmt.Errorf("%w: 缺少 %q 前缀", ErrMalformed, Prefix)
	}
	body := strings.TrimPrefix(plaintext, Prefix)
	if len(body) != bodyLen {
		return fmt.Errorf("%w: 主体长度应为 %d，实际 %d", ErrMalformed, bodyLen, len(body))
	}
	if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
		return fmt.Errorf("%w: 主体不是合法的 base64url", ErrMalformed)
	}
	return nil
}
