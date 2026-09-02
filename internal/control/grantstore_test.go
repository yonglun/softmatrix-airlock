package control

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// seedUserID 造一个用户，返回其 ID。复用 sessionstore_test.go 里已有的
// seedUser(t, UserStore, externalID) *User 助手，避免重复定义。
func seedUserID(t *testing.T, db *sql.DB, externalID string) string {
	t.Helper()
	users := NewPostgresUserStore(db)
	return seedUser(t, users, externalID).ID
}

func TestSyncBuiltinRolesIsIdempotent(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()

	require.NoError(t, s.SyncBuiltinRoles(ctx))
	require.NoError(t, s.SyncBuiltinRoles(ctx), "重复同步不得报错")

	roles, err := s.ListRoles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 6)

	perms, err := s.PermissionsForRole(ctx, authz.RolePlatformAdmin)
	require.NoError(t, err)
	require.Len(t, perms, len(authz.All()), "平台管理员应拥有全部权限")
}

func TestSyncBuiltinRolesRemovesStalePermissions(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))

	// 模拟旧版本残留的一条权限
	_, err := db.ExecContext(ctx,
		`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`,
		authz.RoleDeveloper, "legacy:permission")
	require.NoError(t, err)

	require.NoError(t, s.SyncBuiltinRoles(ctx))

	perms, err := s.PermissionsForRole(ctx, authz.RoleDeveloper)
	require.NoError(t, err)
	require.NotContains(t, perms, "legacy:permission",
		"同步必须清掉代码里已不存在的权限，否则降级后的脏数据会一直留着")
	// 与 Go 侧注册表逐条比对，而不是写死一份清单——
	// 写死会让每次调整角色权限都误伤这条测试，而它真正守的是「脏数据被清掉」。
	require.Equal(t, builtinPermissionsOf(t, authz.RoleDeveloper), perms)
}

// builtinPermissionsOf 返回某内置角色在 Go 注册表里的权限集（已排序）。
func builtinPermissionsOf(t *testing.T, roleID string) []string {
	t.Helper()
	for _, r := range authz.BuiltinRoles() {
		if r.ID == roleID {
			out := append([]string(nil), r.Permissions...)
			sort.Strings(out)
			return out
		}
	}
	t.Fatalf("内置角色 %s 不存在", roleID)
	return nil
}

func TestValidatePermissionsRejectsUnknown(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))

	require.NoError(t, s.ValidatePermissions(ctx))

	_, err := db.ExecContext(ctx,
		`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`,
		authz.RoleAuditor, "bogus:permission")
	require.NoError(t, err)

	require.Error(t, s.ValidatePermissions(ctx),
		"未注册的权限会让某个角色静默少一项能力，必须在启动时炸出来")
}

func TestCreateAndListGrants(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))

	orgs := NewPostgresOrgStore(db)
	require.NoError(t, orgs.Create(ctx, &Org{ID: "rd", Name: "研发中心"}))
	uid := seedUserID(t, db, "u1")

	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: uid, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	got, err := s.ListGrantsForUser(ctx, uid)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, authz.RoleOrgAdmin, got[0].RoleID)
	require.NotNil(t, got[0].OrgID)
	require.Equal(t, "rd", *got[0].OrgID)
}

func TestCreateGrantRejectsDuplicateGlobalGrant(t *testing.T) {
	// Postgres 认为 NULL 彼此不相等，普通唯一索引挡不住重复的全局授予。
	// 迁移里用了 COALESCE(org_id,'') 表达式索引，这里验证它真的生效。
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))
	uid := seedUserID(t, db, "u1")

	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: uid, RoleID: authz.RolePlatformAdmin,
	}))
	require.Error(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g2", UserID: uid, RoleID: authz.RolePlatformAdmin,
	}), "同一用户的同一全局授予不得重复")
}

func TestDeleteGrant(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))
	uid := seedUserID(t, db, "u1")

	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: uid, RoleID: authz.RoleAuditor,
	}))
	require.NoError(t, s.DeleteGrant(ctx, "g1"))
	require.ErrorIs(t, s.DeleteGrant(ctx, "g1"), ErrGrantNotFound)
}

func TestGrantsForUserImplementsAuthzStore(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))

	orgs := NewPostgresOrgStore(db)
	require.NoError(t, orgs.Create(ctx, &Org{ID: "rd", Name: "研发中心"}))
	uid := seedUserID(t, db, "u1")
	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: uid, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	// 直接当 authz.Store 用
	var store authz.Store = s
	grants, err := store.GrantsForUser(ctx, uid)
	require.NoError(t, err)
	require.Len(t, grants, 1)

	path, err := store.OrgPath(ctx, "rd")
	require.NoError(t, err)
	require.Equal(t, "/rd", path)

	_, err = store.OrgPath(ctx, "ghost")
	require.ErrorIs(t, err, authz.ErrOrgNotFound)
}

func TestCountGlobalGrantsOfRole(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRBACStore(db)
	ctx := context.Background()
	require.NoError(t, s.SyncBuiltinRoles(ctx))

	orgs := NewPostgresOrgStore(db)
	require.NoError(t, orgs.Create(ctx, &Org{ID: "rd", Name: "研发中心"}))
	u1 := seedUserID(t, db, "u1")
	u2 := seedUserID(t, db, "u2")

	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: u1, RoleID: authz.RolePlatformAdmin,
	}))
	// 节点级的平台管理员授予不算「系统有管理员」
	require.NoError(t, s.CreateGrant(ctx, RoleGrant{
		ID: "g2", UserID: u2, RoleID: authz.RolePlatformAdmin, OrgID: strp("rd"),
	}))

	n, err := s.CountGlobalGrantsOfRole(ctx, authz.RolePlatformAdmin)
	require.NoError(t, err)
	require.Equal(t, 1, n, "只数全局授予")
}
