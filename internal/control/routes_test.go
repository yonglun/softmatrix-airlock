package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func TestTargetFromPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/rd", nil)
	req.SetPathValue("id", "rd")

	got, err := TargetFromPath("id")(req)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "rd", *got)
}

func TestTargetFromPathMissingIsNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	got, err := TargetFromPath("id")(req)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestTargetFromBodyReadsFieldAndRestoresBody(t *testing.T) {
	// 提取器读了 body 之后必须把它放回去，否则处理器拿到的是空请求体。
	body := `{"name":"研发中心","parent_id":"root"}`
	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(body))

	got, err := TargetFromBody("parent_id")(req)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "root", *got)

	rest, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.JSONEq(t, body, string(rest), "读过的请求体必须还原给处理器")
}

func TestTargetFromBodyNullFieldIsNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":"集团"}`))
	got, err := TargetFromBody("parent_id")(req)
	require.NoError(t, err)
	require.Nil(t, got, "缺字段等同于无目标节点，按建根节点处理")
}

func TestTargetFromBodyInvalidJSONIsNilNotError(t *testing.T) {
	// 请求体不是合法 JSON 是处理器该报的错（400 invalid_body），
	// 不该在判定阶段变成 500。判定阶段按无目标处理，交给全局授予规则。
	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{ not json`))
	got, err := TargetFromBody("parent_id")(req)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestTargetGlobalAlwaysNil(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/orgs/import/preview", nil)
	got, err := TargetGlobal()(req)
	require.NoError(t, err)
	require.Nil(t, got)
}

// enforceFixture 造一个带判定中间件的最小服务器。
func enforceFixture(t *testing.T, route Route) (http.Handler, *fakeUserStore, *fakeSessionStore, *fakeRBACStore) {
	t.Helper()
	users := newFakeUserStore()
	sessions := newFakeSessionStore()
	rbac := newFakeRBACStore()

	auth := NewAuth(AuthDeps{
		Users: users, Sessions: sessions, RBAC: rbac,
		LoginStates: newFakeLoginStateStore(),
		OIDC:        &fakeOIDC{identity: &Identity{Subject: "s1", Email: "a@x.com"}},
	})
	srv := NewServer(ServerDeps{
		Auth:     auth,
		Resolver: authz.NewResolver(rbac),
		Routes:   []Route{route},
	})
	return srv.Handler(), users, sessions, rbac
}

func TestEnforceDeniesWithoutPermission(t *testing.T) {
	h, users, sessions, rbac := enforceFixture(t, Route{
		Pattern: "DELETE /api/orgs/{id}", Access: AccessPermission,
		Permission: authz.PermOrgDelete, Target: TargetFromPath("id"),
		Handler: okHandler,
	})
	rbac.setPath("rd", "/rd")
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/rd", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_denied")
}

func TestEnforceAllowsWithPermission(t *testing.T) {
	h, users, sessions, rbac := enforceFixture(t, Route{
		Pattern: "DELETE /api/orgs/{id}", Access: AccessPermission,
		Permission: authz.PermOrgDelete, Target: TargetFromPath("id"),
		Handler: okHandler,
	})
	rbac.setPath("rd", "/rd")
	c := loggedIn(t, users, sessions)

	u, err := users.ByExternalID(context.Background(), "s1")
	require.NoError(t, err)
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: u.ID, RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/rd", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceRequiresSessionBeforePermission(t *testing.T) {
	h, _, _, _ := enforceFixture(t, Route{
		Pattern: "DELETE /api/orgs/{id}", Access: AccessPermission,
		Permission: authz.PermOrgDelete, Target: TargetFromPath("id"),
		Handler: okHandler,
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/orgs/rd", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code, "未登录应先被会话中间件挡住")
}

func TestEnforceAuthenticatedOnlyNeedsNoPermission(t *testing.T) {
	h, users, sessions, _ := enforceFixture(t, Route{
		Pattern: "GET /api/whoami", Access: AccessAuthenticated, Handler: okHandler,
	})
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceUnknownTargetOrgReturns404(t *testing.T) {
	// 目标节点不存在时应是 404，不该因为「判定拿不到路径」而变成 500。
	h, users, sessions, _ := enforceFixture(t, Route{
		Pattern: "DELETE /api/orgs/{id}", Access: AccessPermission,
		Permission: authz.PermOrgDelete, Target: TargetFromPath("id"),
		Handler: okHandler,
	})
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/ghost", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
