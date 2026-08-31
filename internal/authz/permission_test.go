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
	require.Len(t, all, 10, "权限总数与设计文档 §4 的清单一致")

	for i := 1; i < len(all); i++ {
		require.Less(t, all[i-1].Key, all[i].Key,
			"All() 必须按键排序——遍历 map 的顺序随机，不排序会让预置迁移每次产出不同顺序")
	}
}
