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
