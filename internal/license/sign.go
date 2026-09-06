package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Sign 用私钥签发一份 license 文本。
//
// 这个函数和它需要的私钥都不会出现在客户手上的部署里：私钥由签发方离线
// 保管，二进制里只有公钥。把它放在校验包内是为了让 Sign→Verify 的闭环
// 测试成为可能——两端共用同一套编码规则，任何一边改了另一边立刻暴露。
func Sign(priv ed25519.PrivateKey, p Payload) ([]byte, error) {
	if p.V == 0 {
		p.V = FormatVersion
	}
	if p.V != FormatVersion {
		return nil, fmt.Errorf("%w: 只能签发版本 %d", ErrUnsupportedVersion, FormatVersion)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}

	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("序列化 payload 失败: %w", err)
	}
	sig := ed25519.Sign(priv, body)

	raw := base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
	return []byte(raw), nil
}
