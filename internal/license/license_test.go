package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mint 造一份签好名的 license 文本，供测试使用。
// 每个测试自己生成一对临时密钥，绝不碰内置公钥。
func mint(t *testing.T, p Payload) (ed25519.PublicKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	body, err := json.Marshal(p)
	require.NoError(t, err)
	sig := ed25519.Sign(priv, body)

	raw := base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
	return pub, []byte(raw)
}

func validPayload() Payload {
	return Payload{
		V:         FormatVersion,
		LicenseID: "AL-2026-0007",
		Customer:  "某某银行",
		IssuedAt:  time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2027, 9, 7, 0, 0, 0, 0, time.UTC),
		Seats:     200,
	}
}

func TestTrialIsFiveSeatsWithoutExpiry(t *testing.T) {
	lic := Trial()

	require.Equal(t, StatusTrial, lic.Status)
	require.Equal(t, 5, lic.Seats)
	require.True(t, lic.ExpiresAt.IsZero(), "试用额度不设到期，ExpiresAt 应为零值")
	require.Empty(t, lic.Customer)
}

func TestVerifyAcceptsWellFormedLicense(t *testing.T) {
	pub, raw := mint(t, validPayload())
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	lic, err := VerifyWith(pub, raw, now)

	require.NoError(t, err)
	require.Equal(t, StatusValid, lic.Status)
	require.Equal(t, "某某银行", lic.Customer)
	require.Equal(t, "AL-2026-0007", lic.LicenseID)
	require.Equal(t, 200, lic.Seats)
}

// 整套机制唯一真正承重的断言：改了内容、签名没跟着改，必须被拒。
// 这条过不了，前面所有设计都是装饰。
func TestVerifyRejectsTamperedSeats(t *testing.T) {
	pub, raw := mint(t, validPayload())

	// 把 seats 从 200 改成 9999，签名段原样保留。
	parts := strings.SplitN(string(raw), ".", 2)
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)

	var p Payload
	require.NoError(t, json.Unmarshal(body, &p))
	p.Seats = 9999
	forged, err := json.Marshal(p)
	require.NoError(t, err)

	tampered := base64.RawURLEncoding.EncodeToString(forged) + "." + parts[1]

	_, err = VerifyWith(pub, []byte(tampered), time.Now())
	require.ErrorIs(t, err, ErrBadSignature)
}

func TestVerifyRejectsWrongPublicKey(t *testing.T) {
	_, raw := mint(t, validPayload())
	other, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	_, err = VerifyWith(other, raw, time.Now())
	require.ErrorIs(t, err, ErrBadSignature)
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	cases := map[string]string{
		"没有分隔点":              "eyJ2IjoxfQ",
		"payload 非法 base64": "!!!not-base64!!!.aGVsbG8",
		"签名段非法 base64":       "eyJ2IjoxfQ.!!!not-base64!!!",
		"空字符串":               "",
		"只有一个点":              ".",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyWith(pub, []byte(raw), time.Now())
			require.ErrorIs(t, err, ErrMalformed)
		})
	}
}

func TestVerifyRejectsUnsupportedVersion(t *testing.T) {
	p := validPayload()
	p.V = 2
	pub, raw := mint(t, p)

	_, err := VerifyWith(pub, raw, time.Now())
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	require.Contains(t, err.Error(), "请升级 Airlock")
}

func TestVerifyRejectsInvalidPayload(t *testing.T) {
	cases := map[string]func(*Payload){
		"席位为零":            func(p *Payload) { p.Seats = 0 },
		"席位为负":            func(p *Payload) { p.Seats = -1 },
		"客户名为空":           func(p *Payload) { p.Customer = "" },
		"没有 license_id": func(p *Payload) { p.LicenseID = "" },
		"没有到期时间":          func(p *Payload) { p.ExpiresAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPayload()
			mutate(&p)
			pub, raw := mint(t, p)

			_, err := VerifyWith(pub, raw, time.Now())
			require.ErrorIs(t, err, ErrInvalidPayload)
		})
	}
}

// ExpiresAt 是排他上界。签发工具把 "-expires 2027-09-06" 签成
// 09-07T00:00:00Z，于是 9 月 6 日 23:59:59 仍然有效、9 月 7 日零点整过期。
// 这与 P1.5a 的 parseRangeEnd 是同一个决策（那次真踩过：裸日期解析成
// 当天零点，把「到 9 月 6 日」整天的数据全排除了）。
func TestVerifyExpiryBoundaryIsExclusive(t *testing.T) {
	p := validPayload() // ExpiresAt = 2027-09-07T00:00:00Z
	pub, raw := mint(t, p)

	justBefore := p.ExpiresAt.Add(-time.Nanosecond)
	lic, err := VerifyWith(pub, raw, justBefore)
	require.NoError(t, err)
	require.Equal(t, StatusValid, lic.Status, "到期时刻前一纳秒应仍然有效")

	atExpiry, err := VerifyWith(pub, raw, p.ExpiresAt)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, atExpiry.Status, "到期时刻整点应已过期")
}

// 过期的 license 仍然要能读出客户名与席位数——
// 控制台的红条要显示「授权已于 X 到期」，读不出来就没法提示。
func TestExpiredLicenseStillCarriesItsFields(t *testing.T) {
	p := validPayload()
	pub, raw := mint(t, p)

	lic, err := VerifyWith(pub, raw, p.ExpiresAt.AddDate(0, 0, 3))

	require.NoError(t, err)
	require.Equal(t, StatusExpired, lic.Status)
	require.Equal(t, "某某银行", lic.Customer)
	require.Equal(t, 200, lic.Seats)
	require.Equal(t, p.ExpiresAt, lic.ExpiresAt)
}

func TestTrustedPublicKeyIsWellFormed(t *testing.T) {
	// 内置公钥必须是 32 字节的合法 ed25519 公钥。
	// 贴错一个字符就会让所有真实 license 验签失败，而那种故障
	// 只有在客户机器上才会暴露——所以在这里拦住。
	require.Len(t, trustedPublicKey(), ed25519.PublicKeySize)
}

func TestVerifyUsesTheBuiltInKey(t *testing.T) {
	// 用临时密钥签的 license，内置公钥必须验不过。
	// 这条守的是「有人不小心把测试密钥写进 pubkey.go」。
	_, raw := mint(t, validPayload())

	_, err := Verify(raw, time.Now())
	require.ErrorIs(t, err, ErrBadSignature)
}
