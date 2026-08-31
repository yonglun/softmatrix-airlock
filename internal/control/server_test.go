package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

func newTestServer(t *testing.T) (*Server, *fakeUserStore, *fakeSessionStore) {
	t.Helper()
	users := newFakeUserStore()
	sessions := newFakeSessionStore()
	rbac := newFakeRBACStore()

	auth := NewAuth(AuthDeps{
		Users:       users,
		Sessions:    sessions,
		RBAC:        rbac,
		LoginStates: newFakeLoginStateStore(),
		OIDC:        &fakeOIDC{identity: &Identity{Subject: "s1", Email: "a@x.com"}},
	})
	resolver := authz.NewResolver(rbac)
	srv := NewServer(ServerDeps{
		Auth:     auth,
		OrgAPI:   NewOrgAPI(newFakeOrgStore(), &fakeSource{}, resolver),
		GrantAPI: NewGrantAPI(users, rbac, resolver),
		Resolver: resolver,
	})
	return srv, users, sessions
}

// loggedIn 造一个有效会话并返回对应的 cookie。
func loggedIn(t *testing.T, users *fakeUserStore, sessions *fakeSessionStore) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	u, err := users.Upsert(ctx, &User{ExternalID: "s1", Email: "a@x.com", Status: UserStatusActive})
	require.NoError(t, err)

	token, hash, err := GenerateSessionToken()
	require.NoError(t, err)
	require.NoError(t, sessions.Create(ctx, Session{
		ID: hash, UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))
	return &http.Cookie{Name: sessionCookie, Value: token}
}

func TestServerHealthzNeedsNoAuth(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestServerAuthEndpointsNeedNoSession(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	require.Equal(t, http.StatusFound, rec.Code, "登录入口本身不能要求已登录")
}

func TestServerOrgAPIRequiresSession(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/orgs", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServerOrgAPIListWithoutPermissionIsEmptyNot403(t *testing.T) {
	// GET /api/orgs 是列表接口：登录不等于有权限——这正是 P1.2b 要建立的边界——
	// 但没有可见范围时，正确行为是 200 + 空列表，而不是 403。
	// 中间件曾经因为 TargetFiltered() 恒返回 nil target、被判定为
	// 「无目标节点的节点级权限要求全局授予」而把每个非全局授予的用户
	// 全部拦在 403，这是活的端到端验收（Task 21）才抓到的真实 bug——
	// HandleList 自己的过滤逻辑（含空结果返回 []，见 orgapi_test.go 的
	// TestListIsEmptyWithoutAnyReadAccess）从未被真正跑到过。
	srv, users, sessions := newTestServer(t)
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", rec.Body.String())
}

func TestServerOrgCreateDeniedWithoutPermission(t *testing.T) {
	srv, users, sessions := newTestServer(t)
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":"研发中心"}`))
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServerWhoamiReturnsCurrentUser(t *testing.T) {
	srv, users, sessions := newTestServer(t)
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "a@x.com")
}

func TestServerUnknownPathIs404(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
