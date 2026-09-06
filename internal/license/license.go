// Package license 校验 Airlock 的授权文件。
//
// 这个包只做一件事：把一段文本变成一个可信的 License 值，或者告诉你
// 它不可信。它不碰数据库、不碰 HTTP、不读环境变量，也不知道「当前用了
// 几个席位」——那是管理面查库的事。席位上限是多少由这里说了算。
package license

import (
	"errors"
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
