package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

func TestBootstrapIsSkippedWithoutConfig(t *testing.T) {
	users := newFakeUserStore()
	rbac := newFakeRBACStore()
	a := NewAuth(AuthDeps{Users: users, RBAC: rbac, BootstrapAdmin: ""})
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{ExternalID: "sub-1", Email: "a@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, a.maybeGrantBootstrapAdmin(ctx, u))

	n, err := rbac.CountGlobalGrantsOfRole(ctx, authz.RolePlatformAdmin)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestCheckBootstrapConfigRefusesWithoutAdmin(t *testing.T) {
	require.Error(t, CheckBootstrapConfig(context.Background(), newFakeRBACStore(), ""))
}

func TestCheckBootstrapConfigPassesWithConfig(t *testing.T) {
	require.NoError(t, CheckBootstrapConfig(context.Background(), newFakeRBACStore(), "boss@example.com"))
}
