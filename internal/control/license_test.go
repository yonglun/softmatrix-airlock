package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/license"
)

// countStub 是席位计数的桩：不碰数据库，直接给一个数或一个错误。
type countStub struct {
	n   int
	err error
}

func (c countStub) CountActive(context.Context) (int, error) { return c.n, c.err }

func validLicense(expires time.Time) license.License {
	return license.License{
		Status: license.StatusValid, Customer: "某某银行",
		LicenseID: "AL-1", ExpiresAt: expires, Seats: 3,
	}
}

// 同一个 gate 实例，只换 now，就该从 valid 变 expired——
// 跨过到期日仍在运行的进程必须自己降级，而不是等下次重启。
func TestGateStatusFlipsAtExpiryWithoutRestart(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewLicenseGate(validLicense(expires), countStub{n: 0})

	require.Equal(t, license.StatusValid, g.Status(expires.Add(-time.Hour)))
	require.Equal(t, license.StatusExpired, g.Status(expires))
	require.Equal(t, license.StatusExpired, g.Status(expires.AddDate(0, 0, 7)))
}

func TestGateAllowWriteBlocksOnlyWhenExpired(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewLicenseGate(validLicense(expires), countStub{n: 0})

	require.NoError(t, g.AllowWrite(expires.Add(-time.Hour)))
	require.ErrorIs(t, g.AllowWrite(expires), ErrLicenseExpired)
}

func TestGateTrialAllowsWrites(t *testing.T) {
	g := NewLicenseGate(license.Trial(), countStub{n: 0})

	require.NoError(t, g.AllowWrite(time.Now()))
	require.Equal(t, license.StatusTrial, g.Status(time.Now()))
}

func TestAdmitNewUserRefusesWhenSeatsFull(t *testing.T) {
	g := NewLicenseGate(validLicense(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		countStub{n: 3}) // Seats 也是 3

	reason, err := g.AdmitNewUser(context.Background(), time.Now())

	require.NoError(t, err)
	require.Contains(t, reason, "席位已满")
	require.Contains(t, reason, "3/3")
	require.Contains(t, reason, "停用", "拒绝理由里要带上自助解法")
}

func TestAdmitNewUserAllowsWhenSeatsAvailable(t *testing.T) {
	g := NewLicenseGate(validLicense(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		countStub{n: 2})

	reason, err := g.AdmitNewUser(context.Background(), time.Now())

	require.NoError(t, err)
	require.Empty(t, reason)
}

func TestAdmitNewUserRefusesWhenExpired(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	g := NewLicenseGate(validLicense(expires), countStub{n: 0}) // 席位空着

	reason, err := g.AdmitNewUser(context.Background(), expires)

	require.NoError(t, err)
	require.Contains(t, reason, "过期")
}

func TestGateTrialCapsAtFiveSeats(t *testing.T) {
	g := NewLicenseGate(license.Trial(), countStub{n: 5})

	reason, err := g.AdmitNewUser(context.Background(), time.Now())

	require.NoError(t, err)
	require.Contains(t, reason, "5/5")
}

// 查库失败必须与「被拒绝」分开：前者是 500，后者是 402。
// 混成一个 error 会让数据库抖动表现成「席位已满」，误导所有人。
func TestAdmitNewUserSurfacesCountFailureAsError(t *testing.T) {
	boom := errors.New("数据库炸了")
	g := NewLicenseGate(validLicense(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		countStub{err: boom})

	reason, err := g.AdmitNewUser(context.Background(), time.Now())

	require.Error(t, err)
	require.Empty(t, reason)
}

// nil gate 表示「未装配」，一律放行。这与 ServerDeps 里其它依赖为 nil 时
// 退化成 501 桩是同一种处理：这个仓库对未装配依赖的既定态度是优雅退化，
// 不是 panic。生产装配（app/control.go）一定会设置它。
func TestNilGateAllowsEverything(t *testing.T) {
	var g *LicenseGate

	require.NoError(t, g.AllowWrite(time.Now()))
	reason, err := g.AdmitNewUser(context.Background(), time.Now())
	require.NoError(t, err)
	require.Empty(t, reason)
}
