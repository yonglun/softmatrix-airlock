package authz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupKnownPermission(t *testing.T) {
	p, ok := Lookup(PermOrgRead)
	require.True(t, ok)
	require.Equal(t, PermOrgRead, p.Key)
	require.Equal(t, ScopeOrg, p.Scope)
	require.NotEmpty(t, p.Desc, "每条权限都要有人话说明，控制台要显示")
}

func TestLookupUnknownPermission(t *testing.T) {
	_, ok := Lookup("nonexistent:permission")
	require.False(t, ok)
}

func TestGlobalPermissionsAreMarkedGlobal(t *testing.T) {
	// 这三条是全局能力：全系统只有一份 SSO 配置、一份 License，
	// 成本与审计的读取也不隶属于某个部门。它们必须标为全局，
	// 否则节点级授予能拿到它们——这正是设计文档 D4 要挡的事。
	for _, key := range []string{PermAuditRead, PermCostReadAll, PermPlatformConfigure} {
		p, ok := Lookup(key)
		require.True(t, ok, "权限 %s 未注册", key)
		require.Equal(t, ScopeGlobal, p.Scope, "权限 %s 必须是全局作用域", key)
	}
}

func TestOrgPermissionsAreMarkedOrg(t *testing.T) {
	for _, key := range []string{
		PermOrgRead, PermOrgWrite, PermOrgDelete, PermOrgImport,
		PermMemberAssign, PermGrantRead, PermGrantWrite,
	} {
		p, ok := Lookup(key)
		require.True(t, ok, "权限 %s 未注册", key)
		require.Equal(t, ScopeOrg, p.Scope, "权限 %s 必须是节点作用域", key)
	}
}

func TestAllIsSortedAndComplete(t *testing.T) {
	all := All()
	require.Len(t, all, 14, "权限总数与设计文档 §4 的清单一致（P1.3c 新增 key:revoke_all）")

	for i := 1; i < len(all); i++ {
		require.Less(t, all[i-1].Key, all[i].Key,
			"All() 必须按键排序——遍历 map 的顺序随机，不排序会让预置迁移每次产出不同顺序")
	}
}

func TestKeyPermissionsAreOrgScoped(t *testing.T) {
	// 签发绑定到具体节点（只能给 is_key_holder 的节点签发），
	// 因此这两条必须是节点级——若成了全局权限，
	// 「把 org_admin 授予在某节点上」就会变成全树签发权。
	for _, k := range []string{PermKeyRead, PermKeyWrite} {
		def, ok := Lookup(k)
		require.True(t, ok, "%s 未注册", k)
		require.Equal(t, ScopeOrg, def.Scope)
	}
}

func TestKeyRequestIsOrgScoped(t *testing.T) {
	// 申请绑定到具体节点（只能在自己归属子树内申请），
	// 因此必须是节点级；成了全局权限就等于全树申请权。
	def, ok := Lookup(PermKeyRequest)
	require.True(t, ok, "%s 未注册", PermKeyRequest)
	require.Equal(t, ScopeOrg, def.Scope)
}

func TestKeyRevokeAllIsGlobalScoped(t *testing.T) {
	// 紧急全局吊销不绑定任何节点，必须是全局权限——
	// 若做成 ScopeOrg，在任意一个叶子节点上拿到它就等于能清空全公司。
	def, ok := Lookup(PermKeyRevokeAll)
	require.True(t, ok, "%s 未注册", PermKeyRevokeAll)
	require.Equal(t, ScopeGlobal, def.Scope)
}

func TestKeyRevokeAllIsNotInOrgAdmin(t *testing.T) {
	// 组织管理员管的是自己那棵子树，不该顺带获得清空全系统的能力。
	require.NotContains(t, roleByID(t, RoleOrgAdmin).Permissions, PermKeyRevokeAll)
}
