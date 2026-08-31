package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeStore 是内存版的判定数据源，让判定逻辑完全脱离数据库测试。
type fakeStore struct {
	grants map[string][]Grant // userID -> grants
	paths  map[string]string  // orgID -> 物化路径
	err    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{grants: map[string][]Grant{}, paths: map[string]string{}}
}

func (f *fakeStore) GrantsForUser(_ context.Context, userID string) ([]Grant, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.grants[userID], nil
}

func (f *fakeStore) PermissionsForRole(_ context.Context, roleID string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, r := range BuiltinRoles() {
		if r.ID == roleID {
			return r.Permissions, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) OrgPath(_ context.Context, orgID string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	p, ok := f.paths[orgID]
	if !ok {
		return "", ErrOrgNotFound
	}
	return p, nil
}

// strp 是取字符串地址的小helper。
func strp(s string) *string { return &s }

// activeSubject 造一个活跃用户主体。
func activeSubject(id string) Subject {
	return Subject{UserID: id, Active: true}
}

func TestCanRejectsInactiveSubject(t *testing.T) {
	st := newFakeStore()
	st.grants["u1"] = []Grant{{RoleID: RolePlatformAdmin}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(),
		Subject{UserID: "u1", Active: false}, PermPlatformConfigure, nil)
	require.NoError(t, err)
	require.False(t, ok, "已禁用的用户即使持有平台管理员也不该通过")
}

func TestCanGlobalPermissionViaGlobalGrant(t *testing.T) {
	st := newFakeStore()
	st.grants["u1"] = []Grant{{RoleID: RolePlatformAdmin}} // OrgID 为 nil = 全局
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermPlatformConfigure, nil)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestCanGlobalPermissionDeniedFromScopedGrant(t *testing.T) {
	// 设计文档 D4 的核心性质：把平台管理员授予在某个节点上，
	// 也拿不到 SSO 配置这类全局能力。
	st := newFakeStore()
	st.paths["rd"] = "/root/rd"
	st.grants["u1"] = []Grant{{RoleID: RolePlatformAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermPlatformConfigure, strp("rd"))
	require.NoError(t, err)
	require.False(t, ok, "节点级授予不得赋予全局权限")

	// 连在自己被授予的节点上也不行——全局权限跟节点无关。
	ok, err = r.Can(context.Background(), activeSubject("u1"), PermPlatformConfigure, nil)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCanUnknownPermissionIsDenied(t *testing.T) {
	st := newFakeStore()
	st.grants["u1"] = []Grant{{RoleID: RolePlatformAdmin}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), "nonexistent:perm", nil)
	require.Error(t, err, "未注册的权限是编程错误，要报出来而不是静默拒绝")
	require.False(t, ok)
}

func TestCanPropagatesStoreError(t *testing.T) {
	st := newFakeStore()
	st.err = errors.New("数据库炸了")
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgRead, nil)
	require.Error(t, err, "取数失败必须报错，不能当成「无权限」——那会把故障伪装成正常拒绝")
	require.False(t, ok)
}

// seedTree 造一棵测试用的组织树：
//
//	root
//	├── rd    (/root/rd)
//	├── rd2   (/root/rd2)   同前缀兄弟，用于验证不被误判为 rd 的后代
//	└── rd/gw (/root/rd/gw) rd 的子节点
func seedTree(st *fakeStore) {
	st.paths["root"] = "/root"
	st.paths["rd"] = "/root/rd"
	st.paths["rd2"] = "/root/rd2"
	st.paths["gw"] = "/root/rd/gw"
}

func TestCanScopedGrantAppliesToSelf(t *testing.T) {
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp("rd"))
	require.NoError(t, err)
	require.True(t, ok)
}

func TestCanScopedGrantAppliesToDescendant(t *testing.T) {
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp("gw"))
	require.NoError(t, err)
	require.True(t, ok, "授予在 rd 上，应覆盖其后代 gw")
}

func TestCanScopedGrantDoesNotLeakToSiblingWithSamePrefix(t *testing.T) {
	// P1.2a 在 path 前缀匹配上抓到过这个陷阱：/root/rd 与 /root/rd2
	// 用裸前缀比较会把 rd2 误判成 rd 的后代。这里从判定层再确认一次。
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp("rd2"))
	require.NoError(t, err)
	require.False(t, ok, "rd2 是 rd 的同前缀兄弟，不是它的后代")
}

func TestCanScopedGrantDoesNotApplyToAncestor(t *testing.T) {
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp("root"))
	require.NoError(t, err)
	require.False(t, ok, "管 rd 不等于能管 rd 的上级 root")
}

func TestCanGlobalGrantConfersOrgPermissionEverywhere(t *testing.T) {
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin}} // 全局授予组织管理员
	r := NewResolver(st)

	for _, node := range []string{"root", "rd", "rd2", "gw"} {
		ok, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp(node))
		require.NoError(t, err)
		require.True(t, ok, "全局授予的组织管理员在 %s 上也该有权限", node)
	}
}

func TestCanOrgPermissionWithNilTargetRequiresGlobalGrant(t *testing.T) {
	// 设计文档 §5 的边界情况：建根节点这类操作没有目标节点，
	// 只能由全局授予放行——否则任何节点管理员都能创建平级的新根，
	// 绕出自己的管辖范围。
	st := newFakeStore()
	seedTree(st)
	st.grants["scoped"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	st.grants["global"] = []Grant{{RoleID: RoleOrgAdmin}}
	r := NewResolver(st)

	ok, err := r.Can(context.Background(), activeSubject("scoped"), PermOrgWrite, nil)
	require.NoError(t, err)
	require.False(t, ok, "节点级授予不能创建根节点")

	ok, err = r.Can(context.Background(), activeSubject("global"), PermOrgWrite, nil)
	require.NoError(t, err)
	require.True(t, ok, "全局授予可以创建根节点")
}

func TestCanUnknownTargetOrgReturnsError(t *testing.T) {
	st := newFakeStore()
	seedTree(st)
	st.grants["u1"] = []Grant{{RoleID: RoleOrgAdmin, OrgID: strp("rd")}}
	r := NewResolver(st)

	_, err := r.Can(context.Background(), activeSubject("u1"), PermOrgWrite, strp("nope"))
	require.ErrorIs(t, err, ErrOrgNotFound)
}
