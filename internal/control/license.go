package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/softmatrix/airlock/internal/license"
)

// ErrLicenseExpired 是被只读降级挡下的写操作返回的错误。
// 消息本身就是给用户看的中文——前端直接透传，不再存一份文案。
var ErrLicenseExpired = errors.New("授权已过期，该操作已暂停")

// ActiveUserCounter 是席位闸需要的全部数据库能力。
// 刻意只有一个方法：LicenseGate 不该拿到整个 UserStore。
type ActiveUserCounter interface {
	CountActive(ctx context.Context) (int, error)
}

// LicenseGate 把解析好的 license 与实时席位占用拼在一起，
// 回答管理面的三个问题：现在什么状态、这次写操作放不放、这个新人能不能开户。
//
// 所有方法都显式收 now——测试不必动系统时钟就能跨过到期日。
type LicenseGate struct {
	lic   license.License
	users ActiveUserCounter
}

func NewLicenseGate(lic license.License, users ActiveUserCounter) *LicenseGate {
	return &LicenseGate{lic: lic, users: users}
}

// License 返回解析结果本身（不含实时席位占用）。
func (g *LicenseGate) License() license.License {
	if g == nil {
		return license.Trial()
	}
	return g.lic
}

// Status 按 now 现算状态。启动时有效的 license 跨过到期日后返回 Expired，
// 不需要重启进程。
func (g *LicenseGate) Status(now time.Time) license.Status {
	if g == nil {
		return license.StatusTrial
	}
	if g.lic.Status == license.StatusValid && !now.Before(g.lic.ExpiresAt) {
		return license.StatusExpired
	}
	return g.lic.Status
}

// AllowWrite 在过期时返回 ErrLicenseExpired，供 LicenseRequiresValid 端点使用。
//
// nil 接收者表示依赖未装配，一律放行——与 ServerDeps 里其它依赖为 nil 时
// 退化成 501 桩是同一种处理。生产装配一定会设置它。
func (g *LicenseGate) AllowWrite(now time.Time) error {
	if g == nil {
		return nil
	}
	if g.Status(now) == license.StatusExpired {
		return ErrLicenseExpired
	}
	return nil
}

// AdmitNewUser 判断能否为一个从未建档的新人开户。
//
// 返回值刻意分成两个：reason 非空是「拒绝」，要回 402 并把这句话给用户看；
// error 只表示基础设施故障（查库失败），要回 500。混成一个的话，数据库
// 抖一下就会表现成「席位已满」，把所有人引向错误的排查方向。
func (g *LicenseGate) AdmitNewUser(ctx context.Context, now time.Time) (string, error) {
	if g == nil {
		return "", nil
	}
	if g.Status(now) == license.StatusExpired {
		return "授权已过期，无法开通新账号；已有账号不受影响", nil
	}

	used, err := g.users.CountActive(ctx)
	if err != nil {
		return "", fmt.Errorf("统计席位占用失败: %w", err)
	}
	if used >= g.lic.Seats {
		return fmt.Sprintf(
			"席位已满（%d/%d）；请联系管理员停用离职成员以腾出席位",
			used, g.lic.Seats), nil
	}
	return "", nil
}
