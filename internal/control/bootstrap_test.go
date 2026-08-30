package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootstrapGrantsByEmail(t *testing.T) {
	users := newFakeUserStore()
	a := NewAuth(AuthDeps{Users: users, BootstrapAdmin: "boss@example.com"})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	got, err := users.ByExternalID(ctx, "sub-1")
	require.NoError(t, err)
	require.True(t, got.IsPlatformAdmin)
}

func TestBootstrapGrantsBySubject(t *testing.T) {
	users := newFakeUserStore()
	a := NewAuth(AuthDeps{Users: users, BootstrapAdmin: "sub-1"})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "any@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	got, err := users.ByExternalID(ctx, "sub-1")
	require.NoError(t, err)
	require.True(t, got.IsPlatformAdmin, "配置成 OIDC sub 也应该能匹配上")
}

func TestBootstrapIgnoresOtherUsers(t *testing.T) {
	users := newFakeUserStore()
	a := NewAuth(AuthDeps{Users: users, BootstrapAdmin: "boss@example.com"})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-2", Email: "someone@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	got, err := users.ByExternalID(ctx, "sub-2")
	require.NoError(t, err)
	require.False(t, got.IsPlatformAdmin)
}

func TestBootstrapNoopWhenUnconfigured(t *testing.T) {
	users := newFakeUserStore()
	a := NewAuth(AuthDeps{Users: users, BootstrapAdmin: ""})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	got, err := users.ByExternalID(ctx, "sub-1")
	require.NoError(t, err)
	require.False(t, got.IsPlatformAdmin)
}

func TestBootstrapEmailMatchIsCaseInsensitive(t *testing.T) {
	users := newFakeUserStore()
	a := NewAuth(AuthDeps{Users: users, BootstrapAdmin: "Boss@Example.COM"})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "sub-1", Email: "boss@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	got, err := users.ByExternalID(ctx, "sub-1")
	require.NoError(t, err)
	require.True(t, got.IsPlatformAdmin, "邮箱大小写不该影响匹配")
}

func TestCheckBootstrapConfigRejectsEmptyWhenNoAdmins(t *testing.T) {
	users := newFakeUserStore()
	err := CheckBootstrapConfig(context.Background(), users, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "AIRLOCK_BOOTSTRAP_ADMIN")
}

func TestCheckBootstrapConfigOKWhenAdminExists(t *testing.T) {
	users := newFakeUserStore()
	ctx := context.Background()
	u, err := users.Upsert(ctx, &User{ExternalID: "s", Email: "a@x", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, users.SetPlatformAdmin(ctx, u.ID, true))

	require.NoError(t, CheckBootstrapConfig(ctx, users, ""),
		"已经有管理员了，就不再要求配置 bootstrap")
}

func TestCheckBootstrapConfigOKWhenConfigured(t *testing.T) {
	users := newFakeUserStore()
	require.NoError(t, CheckBootstrapConfig(context.Background(), users, "boss@example.com"))
}
