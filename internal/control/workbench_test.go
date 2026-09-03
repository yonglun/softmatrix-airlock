package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// workbenchFixture 造一棵 root/rd 树与一个 RBAC store。
func workbenchFixture(t *testing.T) (*authz.Resolver, *fakeRBACStore) {
	t.Helper()
	rbac := newFakeRBACStore()
	rbac.setPath("root", "/root")
	rbac.setPath("rd", "/root/rd")
	return authz.NewResolver(rbac), rbac
}

func activeSubj(userID string, primaryOrg *string) authz.Subject {
	return authz.Subject{UserID: userID, Active: true, PrimaryOrgID: primaryOrg}
}

func TestWorkbenchesPlatformAdminSeesThree(t *testing.T) {
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RolePlatformAdmin,
	}))

	got, err := Workbenches(ctx, r, activeSubj("u1", nil))
	require.NoError(t, err)
	require.Equal(t, []string{WorkbenchMySpace, WorkbenchPlatform, WorkbenchFinOps}, got)
}

func TestWorkbenchesSubtreeOrgAdminStillSeesPlatform(t *testing.T) {
	// 这条正是 global_permissions 会漏掉的情形：只在某个节点上持
	// org_admin 的人，全局权限集是空的，但他恰恰是平台管理的目标用户。
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	got, err := Workbenches(ctx, r, activeSubj("u1", nil))
	require.NoError(t, err)
	require.Contains(t, got, WorkbenchPlatform)
	require.Contains(t, got, WorkbenchFinOps, "org_admin 含 key:write，能审批")
}

func TestWorkbenchesPlainDeveloperSeesOnlyMySpace(t *testing.T) {
	// 只有隐式开发者基线（有归属、无授予）的人：基线只给 key:request
	// 与 org:read，两者都不在任何工作台的出现条件里。
	r, _ := workbenchFixture(t)

	got, err := Workbenches(context.Background(), r, activeSubj("u1", strp("rd")))
	require.NoError(t, err)
	require.Equal(t, []string{WorkbenchMySpace}, got)
}

func TestWorkbenchesAnonymousLikeSubjectStillSeesMySpace(t *testing.T) {
	// 没有任何授予、也没有归属的活跃用户，至少能进我的空间——
	// 否则登录成功却无处可去。
	r, _ := workbenchFixture(t)

	got, err := Workbenches(context.Background(), r, activeSubj("u-nobody", nil))
	require.NoError(t, err)
	require.Equal(t, []string{WorkbenchMySpace}, got)
}

func TestWorkbenchesNeverIncludesSecurity(t *testing.T) {
	// 安全合规本期没有任何已实现页面，对谁都不该出现——
	// 与其给一个点进去空无一物的 tab，不如先不放出来。
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RolePlatformAdmin,
	}))

	got, err := Workbenches(ctx, r, activeSubj("u1", nil))
	require.NoError(t, err)
	for _, w := range got {
		require.NotEqual(t, "security", w)
	}
}

func TestWorkbenchesOrderIsStable(t *testing.T) {
	// 顺序稳定，前端渲染的 tab 才不会每次刷新都跳。
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RolePlatformAdmin,
	}))

	for i := 0; i < 5; i++ {
		got, err := Workbenches(ctx, r, activeSubj("u1", nil))
		require.NoError(t, err)
		require.Equal(t, []string{WorkbenchMySpace, WorkbenchPlatform, WorkbenchFinOps}, got)
	}
}
