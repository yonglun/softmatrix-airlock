package license

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 签发→校验闭环：Sign 出来的东西必须能被 VerifyWith 用配对公钥认下。
func TestSignRoundTripsThroughVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	p := Payload{
		V:         FormatVersion,
		LicenseID: "AL-2026-0001",
		Customer:  "测试客户",
		IssuedAt:  time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2027, 9, 7, 0, 0, 0, 0, time.UTC),
		Seats:     50,
	}

	raw, err := Sign(priv, p)
	require.NoError(t, err)

	lic, err := VerifyWith(pub, raw, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, StatusValid, lic.Status)
	require.Equal(t, 50, lic.Seats)
	require.Equal(t, "测试客户", lic.Customer)
}

// seats=0 那种会锁死所有人登录的 license，在签发端就不该造得出来。
func TestSignRejectsInvalidPayload(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	p := Payload{
		V: FormatVersion, LicenseID: "AL-1", Customer: "x",
		ExpiresAt: time.Now().AddDate(1, 0, 0), Seats: 0,
	}

	_, err = Sign(priv, p)
	require.ErrorIs(t, err, ErrInvalidPayload)
}

func TestSignFillsFormatVersion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// 调用方没填 V，Sign 补上当前版本。
	p := Payload{
		LicenseID: "AL-2", Customer: "y",
		ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Seats: 10,
	}

	raw, err := Sign(priv, p)
	require.NoError(t, err)

	lic, err := VerifyWith(pub, raw, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, StatusValid, lic.Status)
}
