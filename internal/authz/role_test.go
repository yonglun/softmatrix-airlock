package authz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func roleByID(t *testing.T, id string) Role {
	t.Helper()
	for _, r := range BuiltinRoles() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("内置角色 %s 不存在", id)
	return Role{}
}

func TestBuiltinRolesCount(t *testing.T) {
	require.Len(t, BuiltinRoles(), 6, "6 个内置角色，与设计文档 §4 一致")
}

func TestEveryRolePermissionIsRegistered(t *testing.T) {
	// 角色里写了一个拼错的权限键，判定时会静默失效——
	// 这个角色会悄悄少一项能力，且没有任何报错。这里挡住。
	for _, r := range BuiltinRoles() {
		require.NotEmpty(t, r.ID)
		require.NotEmpty(t, r.Name)
		require.NotEmpty(t, r.Permissions, "角色 %s 没有任何权限", r.ID)
		for _, p := range r.Permissions {
			require.True(t, IsKnown(p), "角色 %s 引用了未注册的权限 %s", r.ID, p)
		}
	}
}

func TestPlatformAdminHasEveryPermission(t *testing.T) {
	admin := roleByID(t, RolePlatformAdmin)
	held := map[string]bool{}
	for _, p := range admin.Permissions {
		held[p] = true
	}
	for _, p := range All() {
		require.True(t, held[p.Key], "平台管理员缺少权限 %s", p.Key)
	}
}

func TestOrgAdminHasNoGlobalPermission(t *testing.T) {
	// 组织管理员是节点级角色。它一旦含有全局权限，
	// 「把它授予在某节点上」就会变成一条提权路径。
	for _, p := range roleByID(t, RoleOrgAdmin).Permissions {
		def, ok := Lookup(p)
		require.True(t, ok)
		require.Equal(t, ScopeOrg, def.Scope,
			"组织管理员不该持有全局权限 %s", p)
	}
}

func TestDeveloperIsReadOnlyOnOrgTree(t *testing.T) {
	// 开发者是隐式基线角色——任何有归属的用户都自动拥有它。
	// 它的权限集必须极小，否则等于给全公司开了后门。
	require.Equal(t, []string{PermOrgRead}, roleByID(t, RoleDeveloper).Permissions)
}

func TestSecurityOfficerAndAuditorAreCurrentlyIdentical(t *testing.T) {
	// 如实记录现状：区分二者的护栏策略制定、误报申诉要到 P4 才有对应权限。
	// 这个测试不是在固化设计，而是在标记「这里现在确实一样」——
	// P4 给安全合规官加权限时它会失败，提醒改测试而不是悄悄漂移。
	require.Equal(t,
		roleByID(t, RoleSecurityOfficer).Permissions,
		roleByID(t, RoleAuditor).Permissions)
}

func TestBuiltinRolesAreSorted(t *testing.T) {
	roles := BuiltinRoles()
	for i := 1; i < len(roles); i++ {
		require.Less(t, roles[i-1].ID, roles[i].ID,
			"BuiltinRoles() 必须按 ID 排序，保证预置迁移的顺序稳定")
	}
}

func TestOrgAdminCanManageKeys(t *testing.T) {
	perms := roleByID(t, RoleOrgAdmin).Permissions
	require.Contains(t, perms, PermKeyRead)
	require.Contains(t, perms, PermKeyWrite)
}
