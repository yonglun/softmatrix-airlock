package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// approverFixture 造 root/rd/gw 三层树与一个 RBAC store。
func approverFixture(t *testing.T) (*fakeOrgStore, *fakeRBACStore, *fakeUserStore) {
	t.Helper()
	ctx := context.Background()
	orgs := newFakeOrgStore()
	require.NoError(t, orgs.Create(ctx, &Org{ID: "root", Name: "集团"}))
	require.NoError(t, orgs.Create(ctx, &Org{ID: "rd", Name: "研发", ParentID: strp("root")}))
	require.NoError(t, orgs.Create(ctx, &Org{ID: "gw", Name: "网关组", ParentID: strp("rd")}))

	rbac := newFakeRBACStore()
	users := newFakeUserStore()
	return orgs, rbac, users
}

func addUser(t *testing.T, users *fakeUserStore, id, email string) {
	t.Helper()
	_, err := users.Upsert(context.Background(), &User{
		ID: id, ExternalID: id, Email: email, Status: UserStatusActive,
	})
	require.NoError(t, err)
}

func TestApproversIncludesGrantOnTheNodeItself(t *testing.T) {
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	addUser(t, users, "u-gw", "gw@x.com")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-gw", RoleID: authz.RoleOrgAdmin, OrgID: strp("gw"),
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "gw@x.com", got[0].Email)
}

func TestApproversIncludesAncestorGrant(t *testing.T) {
	// 管上级就能管下级——授予在 rd 上的管理员必须能审批 gw 的申请，
	// 否则每加一层子节点都要重新授予一遍。
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	addUser(t, users, "u-rd", "rd@x.com")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-rd", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "rd@x.com", got[0].Email)
}

func TestApproversIncludesGlobalGrant(t *testing.T) {
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	addUser(t, users, "u-plat", "plat@x.com")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-plat", RoleID: authz.RolePlatformAdmin,
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "plat@x.com", got[0].Email)
}

func TestApproversExcludesSiblingBranch(t *testing.T) {
	// 同前缀兄弟节点的陷阱：授予在 rd2 上的人不该能审批 gw 的申请。
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	require.NoError(t, orgs.Create(ctx, &Org{ID: "rd2", Name: "销售", ParentID: strp("root")}))
	addUser(t, users, "u-rd2", "rd2@x.com")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-rd2", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd2"),
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestApproversExcludesRolesWithoutKeyWrite(t *testing.T) {
	// 审计员在 gw 上有授予，但 auditor 不含 key:write，不该被叫来审批。
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	addUser(t, users, "u-aud", "aud@x.com")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-aud", RoleID: authz.RoleAuditor, OrgID: strp("gw"),
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestApproversDeduplicatesAndSkipsDisabled(t *testing.T) {
	orgs, rbac, users := approverFixture(t)
	ctx := context.Background()
	addUser(t, users, "u-both", "both@x.com")
	// 同一个人既有节点授予又有全局授予，只该出现一次。
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: "u-both", RoleID: authz.RoleOrgAdmin, OrgID: strp("gw"),
	}))
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g2", UserID: "u-both", RoleID: authz.RolePlatformAdmin,
	}))
	// 已禁用的用户不该收到通知。
	_, err := users.Upsert(ctx, &User{
		ID: "u-gone", ExternalID: "u-gone", Email: "gone@x.com", Status: UserStatusDisabled,
	})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g3", UserID: "u-gone", RoleID: authz.RoleOrgAdmin, OrgID: strp("gw"),
	}))

	got, err := ApproversOf(ctx, orgs, rbac, users, "gw")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "both@x.com", got[0].Email)
}

func TestApproversUnknownOrg(t *testing.T) {
	orgs, rbac, users := approverFixture(t)
	_, err := ApproversOf(context.Background(), orgs, rbac, users, "nope")
	require.ErrorIs(t, err, ErrOrgNotFound)
}
