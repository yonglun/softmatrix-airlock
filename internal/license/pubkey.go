package license

import (
	"crypto/ed25519"
	"encoding/base64"
)

// trustedPublicKeyBase64 是 Airlock 官方签发公钥（标准 base64，32 字节）。
//
// 刻意不提供任何环境变量或文件来覆盖它：一旦可覆盖，客户换成自己的公钥
// 就能签发任意 license，整套机制归零。测试要注入密钥对时走 VerifyWith
// 的显式参数，而不是开一个生产环境也存在的后门。
//
// 换这个值是不兼容变更：老二进制验不了新 license，新二进制验不了老
// license。轮换时必须给所有现存客户重签。
const trustedPublicKeyBase64 = "1Ss106Fw3t8yLD0DTYWayUgCtGCPEyvUffERt7YRCWM="

// trustedPublicKey 解出内置公钥。常量贴错时这里会 panic——
// 那是构建期就该发现的错误，绝不能拖到客户机器上才表现为「所有 license 都无效」。
func trustedPublicKey() ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(trustedPublicKeyBase64)
	if err != nil {
		panic("internal/license: 内置公钥不是合法 base64: " + err.Error())
	}
	if len(key) != ed25519.PublicKeySize {
		panic("internal/license: 内置公钥长度不对")
	}
	return key
}
