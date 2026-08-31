package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/softmatrix/airlock/internal/authz"
)

// maybeGrantBootstrapAdmin 在用户登录后检查其是否为配置指定的首个管理员。
func (a *Auth) maybeGrantBootstrapAdmin(ctx context.Context, u *User) error {
	want := strings.TrimSpace(a.deps.BootstrapAdmin)
	if want == "" || u == nil || a.deps.RBAC == nil {
		return nil
	}

	n, err := a.deps.RBAC.CountGlobalGrantsOfRole(ctx, authz.RolePlatformAdmin)
	if err != nil {
		return fmt.Errorf("统计平台管理员失败: %w", err)
	}
	if n > 0 {
		return nil
	}

	if !strings.EqualFold(want, u.Email) && want != u.ExternalID {
		return nil
	}

	if err := a.deps.RBAC.CreateGrant(ctx, RoleGrant{
		ID: uuid.NewString(), UserID: u.ID, RoleID: authz.RolePlatformAdmin,
	}); err != nil {
		return fmt.Errorf("授予平台管理员失败: %w", err)
	}
	slog.Info("已授予 bootstrap 平台管理员", "user_id", u.ID, "email", u.Email)
	return nil
}

// CheckBootstrapConfig 在 control 启动时调用。
// 系统里一个管理员都没有、且未配置 bootstrap 时拒绝启动——
// 不允许出现「谁都能登、登进去就是管理员」的窗口期。
func CheckBootstrapConfig(ctx context.Context, rbac RBACStore, bootstrapAdmin string) error {
	if strings.TrimSpace(bootstrapAdmin) != "" {
		return nil
	}
	n, err := rbac.CountGlobalGrantsOfRole(ctx, authz.RolePlatformAdmin)
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
