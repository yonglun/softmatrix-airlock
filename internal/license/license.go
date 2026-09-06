// Package license 校验 Airlock 的授权文件。
//
// 这个包只做一件事：把一段文本变成一个可信的 License 值，或者告诉你
// 它不可信。它不碰数据库、不碰 HTTP、不读环境变量，也不知道「当前用了
// 几个席位」——那是管理面查库的事。席位上限是多少由这里说了算。
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TrialSeats 是未配置 license 时的内置席位上限。
// 够一个小团队把系统跑起来做 POC，不够任何真实部署，
// 所以它既不会挡住评估者，也不会让人忘了买授权。
const TrialSeats = 5

// FormatVersion 是当前支持的 license 格式版本。
// 收到别的版本一律拒绝，绝不「忽略不认识的字段继续跑」——
// 那样将来加的新约束会在老二进制上悄悄失效。
const FormatVersion = 1

type Status string

const (
	// StatusValid 签名有效且未过期。
	StatusValid Status = "valid"
	// StatusExpired 签名有效但已过期。
	StatusExpired Status = "expired"
	// StatusTrial 未配置 license，走内置试用额度。
	StatusTrial Status = "trial"
)

var (
	// ErrMalformed 文件结构不对：缺 "." 分隔，或 base64 解不开。
	ErrMalformed = errors.New("license 文件格式不正确")
	// ErrBadSignature 签名验证失败，含 payload 被篡改的情形。
	ErrBadSignature = errors.New("license 签名校验失败")
	// ErrUnsupportedVersion 格式版本不是 FormatVersion。
	ErrUnsupportedVersion = errors.New("license 格式版本不受支持")
	// ErrInvalidPayload 签名有效，但内容本身不合法（席位非正、客户名为空等）。
	ErrInvalidPayload = errors.New("license 内容不合法")
)

// Payload 是 license 文件里被签名的那段 JSON。
type Payload struct {
	V         int       `json:"v"`
	LicenseID string    `json:"license_id"`
	Customer  string    `json:"customer"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Seats     int       `json:"seats"`
}

// License 是校验后的授权结果。
type License struct {
	Status    Status
	Customer  string
	LicenseID string
	IssuedAt  time.Time
	// ExpiresAt 在 StatusTrial 时为零值，表示不设到期。
	ExpiresAt time.Time
	Seats     int
}

// Trial 返回内置试用额度。未配置 AIRLOCK_LICENSE_FILE 时用它。
func Trial() License {
	return License{Status: StatusTrial, Seats: TrialSeats}
}

// Verify 套用编译进二进制的内置公钥验签。生产路径走这个。
func Verify(raw []byte, now time.Time) (License, error) {
	return VerifyWith(trustedPublicKey(), raw, now)
}

// VerifyWith 用显式公钥验签并解析。生产路径走 Verify，
// 这个导出版本是为了让测试能用临时密钥对，而不必给内置公钥开一个
// 生产环境也存在的覆盖后门。
func VerifyWith(pub ed25519.PublicKey, raw []byte, now time.Time) (License, error) {
	body, sig, err := split(raw)
	if err != nil {
		return License{}, err
	}

	// 先验签，后解析。顺序不能反：JSON 解析器不该接触未经认证的输入。
	if !ed25519.Verify(pub, body, sig) {
		return License{}, ErrBadSignature
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return License{}, fmt.Errorf("%w: payload 不是合法 JSON", ErrInvalidPayload)
	}
	if p.V != FormatVersion {
		return License{}, fmt.Errorf("%w: 收到版本 %d，本版本只认识 %d，请升级 Airlock",
			ErrUnsupportedVersion, p.V, FormatVersion)
	}
	if err := p.Validate(); err != nil {
		return License{}, err
	}

	lic := License{
		Status:    StatusValid,
		Customer:  p.Customer,
		LicenseID: p.LicenseID,
		IssuedAt:  p.IssuedAt,
		ExpiresAt: p.ExpiresAt,
		Seats:     p.Seats,
	}
	// ExpiresAt 是排他上界：签成「次日零点」，于是合同上写的那一天当天仍然可用。
	if !now.Before(p.ExpiresAt) {
		lic.Status = StatusExpired
	}
	return lic, nil
}

// split 把 "payload.signature" 拆成两段解码后的字节。
func split(raw []byte) (body, sig []byte, err error) {
	parts := strings.Split(string(raw), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, nil, fmt.Errorf("%w: 应为 payload.signature 两段", ErrMalformed)
	}
	body, err = base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: payload 段不是合法 base64url", ErrMalformed)
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("%w: 签名段不是合法 base64url", ErrMalformed)
	}
	return body, sig, nil
}

// Validate 检查 payload 内容本身是否合法。
// 签发端与校验端共用，保证「签发工具造不出来的 license，校验端也不认」。
func (p Payload) Validate() error {
	if p.Seats <= 0 {
		return fmt.Errorf("%w: 席位数必须为正，收到 %d", ErrInvalidPayload, p.Seats)
	}
	if p.Customer == "" {
		return fmt.Errorf("%w: 客户名不能为空", ErrInvalidPayload)
	}
	if p.LicenseID == "" {
		return fmt.Errorf("%w: license_id 不能为空", ErrInvalidPayload)
	}
	if p.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: 必须设置到期时间", ErrInvalidPayload)
	}
	return nil
}
