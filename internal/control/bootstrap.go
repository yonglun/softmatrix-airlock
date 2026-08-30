package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// maybeGrantBootstrapAdmin 在用户登录后检查其是否为配置指定的首个管理员。
// 匹配 email（大小写不敏感）或 OIDC sub。
func (a *Auth) maybeGrantBootstrapAdmin(ctx context.Context, u *User) error {
	want := strings.TrimSpace(a.deps.BootstrapAdmin)
	if want == "" || u == nil {
		return nil
	}
	if u.IsPlatformAdmin {
		return nil
	}

	matched := strings.EqualFold(want, u.Email) || want == u.ExternalID
	if !matched {
		return nil
	}

	if err := a.deps.Users.SetPlatformAdmin(ctx, u.ID, true); err != nil {
		return fmt.Errorf("授予平台管理员失败: %w", err)
	}
	u.IsPlatformAdmin = true
	slog.Info("已授予 bootstrap 平台管理员", "user_id", u.ID, "email", u.Email)
	return nil
}

// CheckBootstrapConfig 在 control 启动时调用。
// 系统里一个管理员都没有、且未配置 bootstrap 时拒绝启动——
// 不允许出现「谁都能登、登进去就是管理员」的窗口期。
func CheckBootstrapConfig(ctx context.Context, users UserStore, bootstrapAdmin string) error {
	if strings.TrimSpace(bootstrapAdmin) != "" {
		return nil
	}

	n, err := users.CountPlatformAdmins(ctx)
	if err != nil {
		return fmt.Errorf("统计平台管理员失败: %w", err)
	}
	if n == 0 {
		return errors.New(
			"系统中没有任何平台管理员，且未配置 AIRLOCK_BOOTSTRAP_ADMIN；" +
				"请设置该环境变量（填首个管理员的 email 或 OIDC sub）后重启")
	}
	return nil
}
