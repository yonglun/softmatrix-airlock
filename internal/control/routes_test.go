package control

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

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

func TestEveryRouteDeclaresAccess(t *testing.T) {
	// 机械检查之一：漏声明访问要求就挂。
	// AccessMode 的零值是 AccessUndeclared，忘了写就会停在这里。
	for _, rt := range DefaultRoutes(ServerDeps{}) {
		require.NotEqual(t, AccessUndeclared, rt.Access,
			"路由 %s 没有声明访问要求", rt.Pattern)

		if rt.Access == AccessPermission {
			require.NotEmpty(t, rt.Permission, "路由 %s 声明了需要权限却没写是哪条", rt.Pattern)
			_, known := authz.Lookup(rt.Permission)
			require.True(t, known, "路由 %s 引用了未注册的权限 %s", rt.Pattern, rt.Permission)
			require.NotNil(t, rt.Target, "路由 %s 需要权限判定却没有目标提取器", rt.Pattern)
		}
	}
}

func TestNonPublicRoutesLiveUnderAPIPrefix(t *testing.T) {
	// Handler() 把非公开路由挂在 /api/ 的会话中间件之后。
	// 一条非公开路由若不在 /api/ 下，就会绕过会话校验——必须拦住。
	for _, rt := range DefaultRoutes(ServerDeps{}) {
		if rt.Access == AccessPublic {
			continue
		}
		require.True(t, isAPIPattern(rt.Pattern),
			"非公开路由 %s 不在 /api/ 前缀下，会绕过会话中间件", rt.Pattern)
	}
}

func TestNoRouteRegistrationOutsideTheTable(t *testing.T) {
	// 机械检查之二：堵住绕过路由表直接注册的旁路。
	// 只允许 server.go 的 Handler() 里那两处 mux 注册。
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, f := range files {
		if f == "server.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		require.NotContains(t, string(src), "mux.HandleFunc(",
			"%s 里直接注册了路由，绕过了路由表；请改为在 DefaultRoutes 里声明", f)
		require.NotContains(t, string(src), "mux.Handle(",
			"%s 里直接注册了路由，绕过了路由表；请改为在 DefaultRoutes 里声明", f)
	}
}

func TestDefaultRoutesCoverAllOrgEndpoints(t *testing.T) {
	patterns := map[string]bool{}
	for _, rt := range DefaultRoutes(ServerDeps{}) {
		patterns[rt.Pattern] = true
	}
	// 角色授予相关的路由在 Task 15 追加，届时这份清单也要一并扩充。
	for _, want := range []string{
		"GET /healthz",
		"GET /auth/login",
		"GET /auth/callback",
		"POST /auth/logout",
		"GET /api/whoami",
		"GET /api/orgs",
		"POST /api/orgs",
		"PATCH /api/orgs/{id}/name",
		"PATCH /api/orgs/{id}/parent",
		"DELETE /api/orgs/{id}",
		"PUT /api/orgs/{id}/key-holder",
		"GET /api/orgs/import/preview",
		"POST /api/orgs/import/apply",
		"GET /api/roles",
		"GET /api/orgs/{id}/grants",
		"POST /api/grants",
		"DELETE /api/grants/{id}",
		"PUT /api/users/{id}/primary-org",
		"GET /api/litellm/sync/status",
		"POST /api/litellm/sync",
		"POST /api/keys",
		"GET /api/orgs/{id}/keys",
		"DELETE /api/keys/{id}",
		"POST /api/requests",
		"GET /api/requests",
		"POST /api/requests/{id}/approve",
		"POST /api/requests/{id}/reject",
		"POST /api/requests/{id}/claim",
		"GET /api/requests/to-approve",
		"POST /api/keys/{id}/rotate",
		"POST /api/orgs/{id}/keys/revoke",
		"POST /api/keys/revoke-all",
	} {
		require.True(t, patterns[want], "路由表缺少 %s", want)
	}
}

func TestHandlerDoesNotPanicWithConsoleRoutes(t *testing.T) {
	// SPA 兜底若被写成 "GET /"，会与既有的 "/api/" 冲突，ServeMux
	// 在注册时直接 panic——服务根本起不来。这条当场抓住。
	require.NotPanics(t, func() {
		_ = NewServer(ServerDeps{
			Auth: NewAuth(AuthDeps{
				Users:    newFakeUserStore(),
				Sessions: newFakeSessionStore(),
			}),
			ConsoleFS: fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
			},
		}).Handler()
	})
}

func TestConsoleFallbackDoesNotShadowAPI(t *testing.T) {
	h := NewServer(ServerDeps{
		Auth: NewAuth(AuthDeps{
			Users:    newFakeUserStore(),
			Sessions: newFakeSessionStore(),
		}),
		ConsoleFS: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<html>console</html>")},
		},
	}).Handler()

	// 未登录打 API：应当是 401，而不是被静态兜底接走返回 HTML。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/orgs", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotContains(t, rec.Body.String(), "console")

	// 页面路径仍然回 index.html。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/platform/orgs", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console")
}
