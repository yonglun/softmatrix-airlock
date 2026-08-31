package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// grantFixture 造一个带判定器的 GrantAPI，并预置 visibilityFixture 那棵树。
func grantFixture(t *testing.T) (*GrantAPI, *fakeUserStore, *fakeRBACStore) {
	t.Helper()
	users := newFakeUserStore()
	rbac := newFakeRBACStore()
	rbac.setPath("root", "/root")
	rbac.setPath("rd", "/root/rd")
	rbac.setPath("sales", "/root/sales")
	return NewGrantAPI(users, rbac, authz.NewResolver(rbac)), users, rbac
}

// asUser 把用户塞进请求上下文，模拟 RequireSession 的效果。
func asUser(req *http.Request, u *User) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), userCtxKey, u))
}

func TestListRolesReturnsAllBuiltins(t *testing.T) {
	api, _, _ := grantFixture(t)

	req := asUser(httptest.NewRequest(http.MethodGet, "/api/roles", nil),
		&User{ID: "u1", Status: UserStatusActive})
	rec := httptest.NewRecorder()
	api.HandleListRoles(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got []Role
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 6)
}

func TestCreateGrantSucceedsForSufficientGranter(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	target, err := users.Upsert(ctx, &User{ExternalID: "dev", Email: "d@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	body := `{"user_id":"` + target.ID + `","role_id":"developer","org_id":"rd"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/grants", strings.NewReader(body)), boss)
	rec := httptest.NewRecorder()
	api.HandleCreateGrant(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	got, err := rbac.ListGrantsForUser(ctx, target.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, authz.RoleDeveloper, got[0].RoleID)
}

func TestCreateGrantRejectsEscalation(t *testing.T) {
	// 防提权：组织管理员不能授予含全局权限的平台管理员角色。
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	target, err := users.Upsert(ctx, &User{ExternalID: "dev", Email: "d@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	body := `{"user_id":"` + target.ID + `","role_id":"platform_admin","org_id":"rd"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/grants", strings.NewReader(body)), boss)
	rec := httptest.NewRecorder()
	api.HandleCreateGrant(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "escalation_denied")
}

func TestCreateGrantRejectsUnknownRole(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RolePlatformAdmin,
	}))

	body := `{"user_id":"` + boss.ID + `","role_id":"ghost","org_id":"rd"}`
	req := asUser(httptest.NewRequest(http.MethodPost, "/api/grants", strings.NewReader(body)), boss)
	rec := httptest.NewRecorder()
	api.HandleCreateGrant(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "role_not_found")
}

func TestDeleteGrantChecksPermissionOnTheGrantsNode(t *testing.T) {
	// 撤销授予要判定的是「授予所在的那个节点」，而路径里只有授予 ID。
	// 中间件拿不到目标节点，因此判定下沉到这里。
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	outsider, err := users.Upsert(ctx, &User{ExternalID: "out", Email: "o@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	// 这个人只管 sales，不该能撤销 rd 上的授予
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: outsider.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("sales"),
	}))
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "victim", UserID: "someone", RoleID: authz.RoleDeveloper, OrgID: strp("rd"),
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/grants/victim", nil)
	req.SetPathValue("id", "victim")
	rec := httptest.NewRecorder()
	api.HandleDeleteGrant(rec, asUser(req, outsider))

	require.Equal(t, http.StatusForbidden, rec.Code)

	_, err = rbac.GetGrant(ctx, "victim")
	require.NoError(t, err, "拒绝后授予必须还在")
}

func TestDeleteGrantSucceedsWithPermission(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "victim", UserID: "someone", RoleID: authz.RoleDeveloper, OrgID: strp("rd"),
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/grants/victim", nil)
	req.SetPathValue("id", "victim")
	rec := httptest.NewRecorder()
	api.HandleDeleteGrant(rec, asUser(req, boss))

	require.Equal(t, http.StatusNoContent, rec.Code)
	_, err = rbac.GetGrant(ctx, "victim")
	require.ErrorIs(t, err, ErrGrantNotFound)
}

func TestDeleteUnknownGrantReturns404(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()
	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RolePlatformAdmin,
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/grants/ghost", nil)
	req.SetPathValue("id", "ghost")
	rec := httptest.NewRecorder()
	api.HandleDeleteGrant(rec, asUser(req, boss))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListGrantsForOrg(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()
	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: boss.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/rd/grants", nil)
	req.SetPathValue("id", "rd")
	rec := httptest.NewRecorder()
	api.HandleListGrants(rec, asUser(req, boss))

	require.Equal(t, http.StatusOK, rec.Code)
	var got []RoleGrant
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
}
