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
