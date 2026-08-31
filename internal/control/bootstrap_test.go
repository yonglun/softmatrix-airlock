package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

func bootstrapFixture(t *testing.T, configured string) (*Auth, *fakeUserStore, *fakeRBACStore) {
	t.Helper()
	users := newFakeUserStore()
	rbac := newFakeRBACStore()
	return NewAuth(AuthDeps{Users: users, RBAC: rbac, BootstrapAdmin: configured}), users, rbac
}

func adminCount(t *testing.T, rbac *fakeRBACStore) int {
	t.Helper()
	n, err := rbac.CountGlobalGrantsOfRole(context.Background(), authz.RolePlatformAdmin)
	require.NoError(t, err)
	return n
}

func TestBootstrapGrantsBySubject(t *testing.T) {
	// sub 匹配无条件安全：它由 IdP 分配，用户改不了。
	a, users, rbac := bootstrapFixture(t, "sub-1")
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{ExternalID: "sub-1", Email: "any@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u, Identity{Subject: "sub-1"}))

	require.Equal(t, 1, adminCount(t, rbac))
}

func TestBootstrapGrantsByVerifiedEmail(t *testing.T) {
	a, users, rbac := bootstrapFixture(t, "boss@example.com")
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u,
		Identity{Subject: "sub-1", Email: "boss@example.com", EmailVerified: true}))

	require.Equal(t, 1, adminCount(t, rbac))
}

func TestBootstrapRefusesUnverifiedEmail(t *testing.T) {
	// 复审第 2 条的核心：IdP 允许自助注册时，攻击者可以注册一个
	// email 等于配置值的账号。email 未验证就不认。
	a, users, rbac := bootstrapFixture(t, "boss@example.com")
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "attacker", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u,
		Identity{Subject: "attacker", Email: "boss@example.com", EmailVerified: false}))

	require.Zero(t, adminCount(t, rbac), "未验证的 email 不得触发授予")
}

func TestBootstrapTreatsMissingEmailVerifiedAsUnverified(t *testing.T) {
	// claim 缺失按未验证处理（fail closed）。
	// Identity.EmailVerified 的零值就是 false，这个测试锁住这个行为。
	a, users, rbac := bootstrapFixture(t, "boss@example.com")
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u, Identity{
		Subject: "sub-1", Email: "boss@example.com", // EmailVerified 未设置
	}))

	require.Zero(t, adminCount(t, rbac))
}

func TestBootstrapIsOneShot(t *testing.T) {
	// 系统里已经有管理员之后，这条路径彻底关闭——
	// 即使配置项还留在环境变量里。这是最关键的一改。
	a, users, rbac := bootstrapFixture(t, "boss@example.com")
	ctx := context.Background()

	first, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, first,
		Identity{Subject: "sub-1", Email: "boss@example.com", EmailVerified: true}))
	require.Equal(t, 1, adminCount(t, rbac))

	// 同一个人再登录一次，不该再加一条
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, first,
		Identity{Subject: "sub-1", Email: "boss@example.com", EmailVerified: true}))
	require.Equal(t, 1, adminCount(t, rbac))

	// 另一个 email 相同的账号也拿不到了
	second, err := users.Upsert(ctx, &User{
		ExternalID: "sub-2", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, second,
		Identity{Subject: "sub-2", Email: "boss@example.com", EmailVerified: true}))
	require.Equal(t, 1, adminCount(t, rbac), "已有管理员后不得再触发")
}

func TestBootstrapSkippedWithoutConfig(t *testing.T) {
	a, users, rbac := bootstrapFixture(t, "")
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{ExternalID: "sub-1", Email: "a@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u,
		Identity{Subject: "sub-1", Email: "a@x.com", EmailVerified: true}))

	require.Zero(t, adminCount(t, rbac))
}

func TestCheckBootstrapConfigRefusesWithoutAdmin(t *testing.T) {
	require.Error(t, CheckBootstrapConfig(context.Background(), newFakeRBACStore(), ""))
}

func TestCheckBootstrapConfigPassesWithConfig(t *testing.T) {
	require.NoError(t, CheckBootstrapConfig(context.Background(), newFakeRBACStore(), "boss@example.com"))
}
