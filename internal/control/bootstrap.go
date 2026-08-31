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

// maybeGrantBootstrapAdmin 在装机时授予首个平台管理员。
//
// 三条防线，对应复审第 2 条：
//
//  1. 一次性——只在系统里一个平台管理员都没有时才可能触发。
//     一旦有了管理员，这条路径彻底关闭，即使配置项还留在环境变量里。
//     P1.2a 的版本是每次登录都尝试授予，配置项长期留存就成了长期的提权入口。
//  2. email 必须已验证——IdP 允许自助注册时（Casdoor 默认就允许），
//     攻击者可以注册一个 email 等于配置值的账号。claim 缺失按未验证处理。
//  3. sub 匹配无条件安全——它由 IdP 分配，用户改不了，因此不要求验证标记。
func (a *Auth) maybeGrantBootstrapAdmin(ctx context.Context, u *User, id Identity) error {
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

	matched := false
	switch {
	case want == id.Subject:
		matched = true
	case strings.EqualFold(want, id.Email):
		if !id.EmailVerified {
			slog.Warn("bootstrap 配置的 email 与登录者一致，但 IdP 未标记该 email 已验证，"+
				"拒绝授予；请改用 OIDC sub 作为 AIRLOCK_BOOTSTRAP_ADMIN 的值",
				"email", id.Email, "subject", id.Subject)
			return nil
		}
		matched = true
	}
	if !matched {
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
