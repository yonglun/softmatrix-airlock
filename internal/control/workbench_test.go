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

func TestWorkbenchesPlatformAdminSeesAll(t *testing.T) {
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RolePlatformAdmin,
	}))

	got, err := Workbenches(ctx, r, activeSubj("u1", nil))
	require.NoError(t, err)
	require.Equal(t,
		[]string{WorkbenchMySpace, WorkbenchPlatform, WorkbenchFinOps, WorkbenchSecurity},
		got, "安全合规在 P1.5a 第一次出现，平台管理员看得到全部四个")
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

func TestWorkbenchesSecurityRequiresAuditRead(t *testing.T) {
	// 安全合规现在有了第一个已实现页面（审计检索），但仍然不该对
	// 没有 audit:read 的人出现——与其它工作台同一条纪律：可见性
	// 严格等于「有页面可用」。
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	got, err := Workbenches(ctx, r, activeSubj("u1", nil))
	require.NoError(t, err)
	for _, w := range got {
		require.NotEqual(t, WorkbenchSecurity, w, "org_admin 没有 audit:read，不该看到安全合规")
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
		require.Equal(t,
			[]string{WorkbenchMySpace, WorkbenchPlatform, WorkbenchFinOps, WorkbenchSecurity}, got)
	}
}

func TestWorkbenchFinOpsVisibleToCostReaderWithoutKeyWrite(t *testing.T) {
	// 这条修的是一个既存缺陷：finops 角色只有 cost:read_all + org:read，
	// 没有 key:write，因此在本期之前根本看不到成本财务工作台——
	// 而那正是为他造的工作台。
	r, rbac := workbenchFixture(t)
	ctx := context.Background()
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g-finops", UserID: "u-fin", RoleID: authz.RoleFinOps,
	}))

	got, err := Workbenches(ctx, r, activeSubj("u-fin", nil))
	require.NoError(t, err)
	require.Contains(t, got, WorkbenchFinOps)
}

func TestWorkbenchSecurityNeedsAuditRead(t *testing.T) {
	r, rbac := workbenchFixture(t)
	ctx := context.Background()

	// 只在节点上持 org_admin 的人：有 cost:read，但没有 audit:read。
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g-rd", UserID: "u-rd", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))
	got, err := Workbenches(ctx, r, activeSubj("u-rd", nil))
	require.NoError(t, err)
	require.NotContains(t, got, WorkbenchSecurity, "没有审计权限就不该出现安全合规")
	require.Contains(t, got, WorkbenchFinOps, "但有 cost:read，成本财务要出现")

	// 安全合规官持全局 audit:read。
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g-sec", UserID: "u-sec", RoleID: authz.RoleSecurityOfficer,
	}))
	got2, err := Workbenches(ctx, r, activeSubj("u-sec", nil))
	require.NoError(t, err)
	require.Contains(t, got2, WorkbenchSecurity)
}
