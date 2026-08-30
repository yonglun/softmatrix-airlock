package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) (*Server, *fakeUserStore, *fakeSessionStore) {
	t.Helper()
	users := newFakeUserStore()
	sessions := newFakeSessionStore()

	auth := NewAuth(AuthDeps{
		Users:       users,
		Sessions:    sessions,
		LoginStates: newFakeLoginStateStore(),
		OIDC:        &fakeOIDC{identity: &Identity{Subject: "s1", Email: "a@x.com"}},
	})
	srv := NewServer(ServerDeps{
		Auth:   auth,
		OrgAPI: NewOrgAPI(newFakeOrgStore(), &fakeSource{}),
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

func TestServerOrgAPIAllowsAuthenticated(t *testing.T) {
	srv, users, sessions := newTestServer(t)
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestServerOrgCreateRoute(t *testing.T) {
	srv, users, sessions := newTestServer(t)
	c := loggedIn(t, users, sessions)

	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":"研发中心"}`))
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
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
