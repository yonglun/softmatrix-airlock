package control

import (
	"context"
	"sync"
	"time"

	"github.com/softmatrix/airlock/internal/authz"
)

// fakeRBACStore 是内存版 RBAC 存储，供不需要数据库的测试使用。
type fakeRBACStore struct {
	mu     sync.Mutex
	grants map[string]RoleGrant // grantID -> grant
	paths  map[string]string    // orgID -> 物化路径
}

func newFakeRBACStore() *fakeRBACStore {
	return &fakeRBACStore{grants: map[string]RoleGrant{}, paths: map[string]string{}}
}

func (f *fakeRBACStore) SyncBuiltinRoles(context.Context) error    { return nil }
func (f *fakeRBACStore) ValidatePermissions(context.Context) error { return nil }

func (f *fakeRBACStore) ListRoles(context.Context) ([]Role, error) {
	var out []Role
	for _, r := range authz.BuiltinRoles() {
		out = append(out, Role{ID: r.ID, Name: r.Name, Description: r.Description, IsBuiltin: true})
	}
	return out, nil
}

func (f *fakeRBACStore) CreateGrant(_ context.Context, g RoleGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g.CreatedAt = time.Now()
	f.grants[g.ID] = g
	return nil
}

func (f *fakeRBACStore) GetGrant(_ context.Context, id string) (RoleGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.grants[id]
	if !ok {
		return RoleGrant{}, ErrGrantNotFound
	}
	return g, nil
}

func (f *fakeRBACStore) DeleteGrant(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.grants[id]; !ok {
		return ErrGrantNotFound
	}
	delete(f.grants, id)
	return nil
}

func (f *fakeRBACStore) ListGrantsForUser(_ context.Context, userID string) ([]RoleGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RoleGrant
	for _, g := range f.grants {
		if g.UserID == userID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeRBACStore) ListGrantsForOrg(_ context.Context, orgID string) ([]RoleGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RoleGrant
	for _, g := range f.grants {
		if g.OrgID != nil && *g.OrgID == orgID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeRBACStore) CountGlobalGrantsOfRole(_ context.Context, roleID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, g := range f.grants {
		if g.RoleID == roleID && g.OrgID == nil {
			n++
		}
	}
	return n, nil
}

// ---- authz.Store 实现 ----

func (f *fakeRBACStore) GrantsForUser(_ context.Context, userID string) ([]authz.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []authz.Grant
	for _, g := range f.grants {
		if g.UserID == userID {
			out = append(out, authz.Grant{RoleID: g.RoleID, OrgID: g.OrgID})
		}
	}
	return out, nil
}

func (f *fakeRBACStore) PermissionsForRole(_ context.Context, roleID string) ([]string, error) {
	for _, r := range authz.BuiltinRoles() {
		if r.ID == roleID {
			return r.Permissions, nil
		}
	}
	return nil, nil
}

func (f *fakeRBACStore) OrgPath(_ context.Context, orgID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.paths[orgID]
	if !ok {
		return "", authz.ErrOrgNotFound
	}
	return p, nil
}

// setPath 在测试里登记一个节点的物化路径。
func (f *fakeRBACStore) setPath(orgID, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths[orgID] = path
}
