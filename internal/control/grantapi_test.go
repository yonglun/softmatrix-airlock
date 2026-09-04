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

func TestWhoamiReturnsProfileGrantsAndGlobalPermissions(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "s1", Email: "a@x.com", DisplayName: "阿三", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: u.ID, RoleID: authz.RoleFinOps,
	}))

	req := asUser(httptest.NewRequest(http.MethodGet, "/api/whoami", nil), u)
	rec := httptest.NewRecorder()
	api.HandleWhoami(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		User              User        `json:"user"`
		Grants            []RoleGrant `json:"grants"`
		GlobalPermissions []string    `json:"global_permissions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Equal(t, "a@x.com", got.User.Email)
	require.Len(t, got.Grants, 1)
	require.Contains(t, got.GlobalPermissions, authz.PermCostReadAll)
	require.NotContains(t, got.GlobalPermissions, authz.PermPlatformConfigure)
}

func TestWhoamiGlobalPermissionsAreSorted(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()
	u, err := users.Upsert(ctx, &User{ExternalID: "s1", Email: "a@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: u.ID, RoleID: authz.RolePlatformAdmin,
	}))

	req := asUser(httptest.NewRequest(http.MethodGet, "/api/whoami", nil), u)
	rec := httptest.NewRecorder()
	api.HandleWhoami(rec, req)

	var got struct {
		GlobalPermissions []string `json:"global_permissions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	for i := 1; i < len(got.GlobalPermissions); i++ {
		require.Less(t, got.GlobalPermissions[i-1], got.GlobalPermissions[i],
			"权限列表要排序，否则前端每次渲染顺序都不一样")
	}
}

func TestAssignPrimaryOrgSetsAndClears(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	target, err := users.Upsert(ctx, &User{ExternalID: "dev", Email: "d@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	assign := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut,
			"/api/users/"+target.ID+"/primary-org", strings.NewReader(body))
		req.SetPathValue("id", target.ID)
		rec := httptest.NewRecorder()
		api.HandleAssignPrimaryOrg(rec, asUser(req, boss))
		return rec
	}

	require.Equal(t, http.StatusNoContent, assign(`{"org_id":"rd"}`).Code)
	got, err := users.ByID(ctx, target.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PrimaryOrgID)
	require.Equal(t, "rd", *got.PrimaryOrgID)

	require.Equal(t, http.StatusNoContent, assign(`{"org_id":null}`).Code)
	got, err = users.ByID(ctx, target.ID)
	require.NoError(t, err)
	require.Nil(t, got.PrimaryOrgID)
}

func TestAssignPrimaryOrgUnknownUserReturns404(t *testing.T) {
	api, users, rbac := grantFixture(t)
	ctx := context.Background()
	boss, err := users.Upsert(ctx, &User{ExternalID: "boss", Email: "b@x.com", Status: UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g0", UserID: boss.ID, RoleID: authz.RolePlatformAdmin,
	}))

	req := httptest.NewRequest(http.MethodPut, "/api/users/ghost/primary-org",
		strings.NewReader(`{"org_id":"rd"}`))
	req.SetPathValue("id", "ghost")
	rec := httptest.NewRecorder()
	api.HandleAssignPrimaryOrg(rec, asUser(req, boss))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWhoamiIncludesWorkbenches(t *testing.T) {
	// grantFixture 已经预置了 root/rd/sales 三条路径。
	api, users, rbac := grantFixture(t)
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{
		ExternalID: "s1", Email: "a@x.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g1", UserID: u.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	rec := httptest.NewRecorder()
	api.HandleWhoami(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/whoami", nil), u))

	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Workbenches       []string `json:"workbenches"`
		GlobalPermissions []string `json:"global_permissions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Contains(t, got.Workbenches, "platform",
		"只在某节点持 org_admin 的用户也要能看到平台管理")
	require.Empty(t, got.GlobalPermissions,
		"同一个用户的全局权限集是空的——这正是不能用它判定工作台的原因")
}

func TestEffectiveGrantsViewCarriesSource(t *testing.T) {
	api, _, rbac := grantFixture(t)
	root := "root"
	rd := "rd"
	rbac.setEffective("rd", []EffectiveGrant{
		{RoleGrant: RoleGrant{ID: "g1", UserID: "u1", RoleID: "org_admin", OrgID: &rd},
			Source: GrantSourceDirect},
		{RoleGrant: RoleGrant{ID: "g2", UserID: "u2", RoleID: "auditor", OrgID: &root},
			Source: GrantSourceInherited},
		{RoleGrant: RoleGrant{ID: "g3", UserID: "u3", RoleID: "platform_admin", OrgID: nil},
			Source: GrantSourceGlobal},
	})

	req := asUser(httptest.NewRequest(http.MethodGet, "/api/orgs/rd/effective-grants", nil),
		&User{ID: "admin", Status: UserStatusActive})
	req.SetPathValue("id", "rd")
	rec := httptest.NewRecorder()
	api.HandleEffectiveGrants(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got []struct {
		ID          string  `json:"id"`
		Source      string  `json:"source"`
		SourceOrgID *string `json:"source_org_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 3)
	require.Equal(t, "direct", got[0].Source)
	require.Equal(t, "rd", *got[0].SourceOrgID)
	require.Equal(t, "inherited", got[1].Source)
	require.Equal(t, "root", *got[1].SourceOrgID, "继承行要指出授予实际挂在哪个节点")
	require.Equal(t, "global", got[2].Source)
	require.Nil(t, got[2].SourceOrgID, "全局授予没有来源节点")
}

func TestEffectiveGrantsUnknownOrgIs404(t *testing.T) {
	api, _, rbac := grantFixture(t)
	rbac.effectiveErr = ErrOrgNotFound

	req := asUser(httptest.NewRequest(http.MethodGet, "/api/orgs/nope/effective-grants", nil),
		&User{ID: "admin", Status: UserStatusActive})
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	api.HandleEffectiveGrants(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "org_not_found")
}

func rolesReq(t *testing.T, api *GrantAPI, u *User, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/roles"+query, nil), u)
	rec := httptest.NewRecorder()
	api.HandleListRoles(rec, req)
	return rec
}

func roleIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var roles []struct {
		ID string `json:"ID"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &roles))
	out := []string{}
	for _, r := range roles {
		out = append(out, r.ID)
	}
	return out
}

func TestListRolesGrantableAtFiltersByAntiEscalation(t *testing.T) {
	// 只在 rd 上持 org_admin 的人，不能把 platform_admin 授给别人——
	// 那会让他间接拿到自己没有的全局权限。下拉里就不该出现这个选项。
	api, _, rbac := grantFixture(t)
	_ = rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-orgadmin", UserID: "boss", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	})
	boss := &User{ID: "boss", Status: UserStatusActive}

	all := roleIDs(t, rolesReq(t, api, boss, ""))
	require.Contains(t, all, authz.RolePlatformAdmin, "不带参数时行为不变：返回全部角色")

	grantable := roleIDs(t, rolesReq(t, api, boss, "?grantable_at=rd"))
	require.NotContains(t, grantable, authz.RolePlatformAdmin,
		"授不了的角色不能出现在下拉里")
	require.Contains(t, grantable, authz.RoleOrgAdmin, "自己持有的角色可以往下授")
}

func TestListRolesGrantableAtForPlatformAdminKeepsEverything(t *testing.T) {
	api, _, rbac := grantFixture(t)
	_ = rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-platform", UserID: "root", RoleID: authz.RolePlatformAdmin,
	})

	grantable := roleIDs(t, rolesReq(t,
		api, &User{ID: "root", Status: UserStatusActive}, "?grantable_at=rd"))
	require.Contains(t, grantable, authz.RolePlatformAdmin)
}

func TestListUsersNeedsGrantReadSomewhere(t *testing.T) {
	// 门槛是「在任意位置持有 grant:read」而不是全局 grant:read：
	// grant:read 是 ScopeOrg 权限，组织管理员是在节点上持有它的，
	// 用全局视角判定会把这两个页面的主要用户全部挡在门外。
	api, users, rbac := grantFixture(t)
	addUser(t, users, "someone", "someone@x.com")

	nobody := &User{ID: "nobody", Status: UserStatusActive}
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/users", nil), nobody)
	rec := httptest.NewRecorder()
	api.HandleListUsers(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, "一条授予都没有的人看不到通讯录")

	// 只在 rd 这一个节点上持 org_admin（含 grant:read）就够了。
	_ = rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-rd-admin", UserID: "boss", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	})
	req2 := asUser(httptest.NewRequest(http.MethodGet, "/api/users", nil),
		&User{ID: "boss", Status: UserStatusActive})
	rec2 := httptest.NewRecorder()
	api.HandleListUsers(rec2, req2)

	require.Equal(t, http.StatusOK, rec2.Code)
	var got []struct {
		ID    string `json:"ID"`
		Email string `json:"Email"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &got))
	require.NotEmpty(t, got, "节点级的 grant:read 就足以拿到用户列表")
}
