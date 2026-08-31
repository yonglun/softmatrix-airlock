package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---- 内存假实现 ----

type fakeUserStore struct {
	mu    sync.Mutex
	byExt map[string]*User
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{byExt: map[string]*User{}}
}

func (f *fakeUserStore) ByID(_ context.Context, id string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byExt {
		if u.ID == id {
			cp := *u
			return &cp, nil
		}
	}
	return nil, ErrUserNotFound
}

func (f *fakeUserStore) ByExternalID(_ context.Context, ext string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byExt[ext]
	if !ok {
		return nil, ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeUserStore) Upsert(_ context.Context, u *User) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.byExt[u.ExternalID]
	if ok {
		existing.Email = u.Email
		existing.DisplayName = u.DisplayName
		existing.LastLoginAt = u.LastLoginAt
		cp := *existing
		return &cp, nil
	}
	cp := *u
	if cp.ID == "" {
		cp.ID = "uid-" + u.ExternalID
	}
	if cp.Status == "" {
		cp.Status = UserStatusActive
	}
	f.byExt[u.ExternalID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeUserStore) ListActive(context.Context) ([]*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*User
	for _, u := range f.byExt {
		if u.Status == UserStatusActive {
			cp := *u
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeUserStore) MarkDisabled(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byExt {
		for _, id := range ids {
			if u.ID == id {
				u.Status = UserStatusDisabled
			}
		}
	}
	return nil
}

func (f *fakeUserStore) AssignPrimaryOrg(_ context.Context, id string, orgID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byExt {
		if u.ID == id {
			u.PrimaryOrgID = orgID
			return nil
		}
	}
	return ErrUserNotFound
}

func (f *fakeUserStore) CountByPrimaryOrg(_ context.Context, orgID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, u := range f.byExt {
		if u.PrimaryOrgID != nil && *u.PrimaryOrgID == orgID {
			n++
		}
	}
	return n, nil
}

type fakeSessionStore struct {
	mu   sync.Mutex
	data map[string]Session
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{data: map[string]Session{}}
}

func (f *fakeSessionStore) Create(_ context.Context, s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[s.ID] = s
	return nil
}

func (f *fakeSessionStore) Get(_ context.Context, id string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.data[id]
	if !ok || !s.ExpiresAt.After(time.Now()) {
		return nil, ErrSessionNotFound
	}
	cp := s
	return &cp, nil
}

func (f *fakeSessionStore) Touch(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.data[id]; ok {
		s.LastSeenAt = at
		f.data[id] = s
	}
	return nil
}

func (f *fakeSessionStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
	return nil
}

func (f *fakeSessionStore) DeleteByUser(_ context.Context, uid string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, s := range f.data {
		if s.UserID == uid {
			delete(f.data, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeSessionStore) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeLoginStateStore struct {
	mu   sync.Mutex
	data map[string]LoginState
}

func newFakeLoginStateStore() *fakeLoginStateStore {
	return &fakeLoginStateStore{data: map[string]LoginState{}}
}

func (f *fakeLoginStateStore) Create(_ context.Context, ls LoginState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[ls.ID] = ls
	return nil
}

func (f *fakeLoginStateStore) Take(_ context.Context, id string) (*LoginState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ls, ok := f.data[id]
	if !ok || !ls.ExpiresAt.After(time.Now()) {
		return nil, ErrLoginStateNotFound
	}
	delete(f.data, id)
	cp := ls
	return &cp, nil
}

func (f *fakeLoginStateStore) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeOIDC struct {
	identity  *Identity
	exchanged struct{ code, verifier string }
	err       error
}

func (f *fakeOIDC) AuthCodeURL(state, challenge string) string {
	return "https://idp.example.com/authorize?state=" + url.QueryEscape(state) +
		"&code_challenge=" + url.QueryEscape(challenge)
}

func (f *fakeOIDC) Exchange(_ context.Context, code, verifier string) (*Identity, error) {
	f.exchanged.code = code
	f.exchanged.verifier = verifier
	if f.err != nil {
		return nil, f.err
	}
	return f.identity, nil
}

func newTestAuth(t *testing.T) (*Auth, *fakeUserStore, *fakeSessionStore, *fakeLoginStateStore, *fakeOIDC) {
	t.Helper()
	users := newFakeUserStore()
	sessions := newFakeSessionStore()
	states := newFakeLoginStateStore()
	oidcClient := &fakeOIDC{identity: &Identity{
		Subject: "sub-1", Email: "zhang@example.com", DisplayName: "张伟",
	}}

	a := NewAuth(AuthDeps{
		Users:          users,
		Sessions:       sessions,
		LoginStates:    states,
		OIDC:           oidcClient,
		BootstrapAdmin: "",
		SecureCookie:   false,
	})
	return a, users, sessions, states, oidcClient
}

// ---- 测试 ----

func TestLoginRedirectsToIdPAndStoresState(t *testing.T) {
	a, _, _, states, _ := newTestAuth(t)

	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	require.Equal(t, http.StatusFound, rec.Code)
	loc := rec.Header().Get("Location")
	require.Contains(t, loc, "https://idp.example.com/authorize")
	require.Contains(t, loc, "state=")
	require.Contains(t, loc, "code_challenge=")

	require.Len(t, states.data, 1, "必须把 state 与 PKCE verifier 存到服务端")

	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == loginStateCookie {
			found = true
			require.True(t, c.HttpOnly, "登录状态 cookie 必须 HttpOnly")
		}
	}
	require.True(t, found, "必须下发登录状态 cookie")
}

func TestCallbackCreatesSessionAndUser(t *testing.T) {
	a, users, sessions, states, oidcClient := newTestAuth(t)

	// 先走一遍 login 拿到 state 与 cookie
	loginRec := httptest.NewRecorder()
	a.HandleLogin(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))

	var lsID, state string
	for id, ls := range states.data {
		lsID, state = id, ls.State
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: loginStateCookie, Value: lsID})
	rec := httptest.NewRecorder()
	a.HandleCallback(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "c1", oidcClient.exchanged.code)
	require.NotEmpty(t, oidcClient.exchanged.verifier, "必须把 PKCE verifier 送去交换")

	u, err := users.ByExternalID(context.Background(), "sub-1")
	require.NoError(t, err)
	require.Equal(t, "zhang@example.com", u.Email)
	require.NotNil(t, u.LastLoginAt)

	require.Len(t, sessions.data, 1)

	var sessCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			sessCookie = c
		}
	}
	require.NotNil(t, sessCookie)
	require.True(t, sessCookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sessCookie.SameSite)
	require.NotContains(t, sessions.data, sessCookie.Value,
		"数据库里存的必须是哈希，不能是 cookie 里的原始 token")
	require.Contains(t, sessions.data, HashSessionToken(sessCookie.Value))
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	a, _, sessions, states, _ := newTestAuth(t)

	loginRec := httptest.NewRecorder()
	a.HandleLogin(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var lsID string
	for id := range states.data {
		lsID = id
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state=WRONG", nil)
	req.AddCookie(&http.Cookie{Name: loginStateCookie, Value: lsID})
	rec := httptest.NewRecorder()
	a.HandleCallback(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, sessions.data, "state 不匹配绝不能建会话")
}

func TestCallbackRejectsMissingLoginStateCookie(t *testing.T) {
	a, _, sessions, _, _ := newTestAuth(t)

	rec := httptest.NewRecorder()
	a.HandleCallback(rec, httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state=s", nil))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, sessions.data)
}

func TestCallbackRejectsReplayedLoginState(t *testing.T) {
	a, _, _, states, _ := newTestAuth(t)

	loginRec := httptest.NewRecorder()
	a.HandleLogin(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var lsID, state string
	for id, ls := range states.data {
		lsID, state = id, ls.State
	}

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state="+url.QueryEscape(state), nil)
		r.AddCookie(&http.Cookie{Name: loginStateCookie, Value: lsID})
		return r
	}

	first := httptest.NewRecorder()
	a.HandleCallback(first, newReq())
	require.Equal(t, http.StatusFound, first.Code)

	second := httptest.NewRecorder()
	a.HandleCallback(second, newReq())
	require.Equal(t, http.StatusBadRequest, second.Code, "同一登录状态不得被用第二次")
}

func TestCallbackRejectsDisabledUser(t *testing.T) {
	a, users, sessions, states, _ := newTestAuth(t)

	// 预置一个已禁用的同 sub 用户
	_, err := users.Upsert(context.Background(), &User{
		ExternalID: "sub-1", Email: "zhang@example.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	require.NoError(t, users.MarkDisabled(context.Background(), []string{"uid-sub-1"}))

	loginRec := httptest.NewRecorder()
	a.HandleLogin(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var lsID, state string
	for id, ls := range states.data {
		lsID, state = id, ls.State
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: loginStateCookie, Value: lsID})
	rec := httptest.NewRecorder()
	a.HandleCallback(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code,
		"IdP 放行但 Airlock 侧已禁用的用户必须挡住——这是对账窗口期的补充防线")
	require.Empty(t, sessions.data)
}

func TestCallbackSurfacesExchangeFailure(t *testing.T) {
	a, _, sessions, states, oidcClient := newTestAuth(t)
	oidcClient.err = errors.New("boom")

	loginRec := httptest.NewRecorder()
	a.HandleLogin(loginRec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	var lsID, state string
	for id, ls := range states.data {
		lsID, state = id, ls.State
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: loginStateCookie, Value: lsID})
	rec := httptest.NewRecorder()
	a.HandleCallback(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Empty(t, sessions.data)
}

func TestLogoutDeletesSessionAndClearsCookie(t *testing.T) {
	a, _, sessions, _, _ := newTestAuth(t)

	token, hash, err := GenerateSessionToken()
	require.NoError(t, err)
	require.NoError(t, sessions.Create(context.Background(), Session{
		ID: hash, UserID: "u1",
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	a.HandleLogout(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, sessions.data)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			require.Equal(t, -1, c.MaxAge, "登出必须让 cookie 立即失效")
		}
	}
}

func TestRequireSessionAllowsValidSession(t *testing.T) {
	a, users, sessions, _, _ := newTestAuth(t)

	u, err := users.Upsert(context.Background(), &User{
		ExternalID: "sub-9", Email: "u@x.com", Status: UserStatusActive,
	})
	require.NoError(t, err)

	token, hash, err := GenerateSessionToken()
	require.NoError(t, err)
	require.NoError(t, sessions.Create(context.Background(), Session{
		ID: hash, UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))

	h := a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := UserFromContext(r.Context())
		require.True(t, ok)
		_, _ = w.Write([]byte(got.Email))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "u@x.com", rec.Body.String())
}

func TestRequireSessionRejectsMissingAndBadCookie(t *testing.T) {
	a, _, _, _, _ := newTestAuth(t)
	h := a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	noCookie := httptest.NewRecorder()
	h.ServeHTTP(noCookie, httptest.NewRequest(http.MethodGet, "/api/orgs", nil))
	require.Equal(t, http.StatusUnauthorized, noCookie.Code)

	badReq := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	badReq.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-real-token"})
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, badReq)
	require.Equal(t, http.StatusUnauthorized, bad.Code)
}

func TestRequireSessionRejectsDisabledUser(t *testing.T) {
	a, users, sessions, _, _ := newTestAuth(t)

	u, err := users.Upsert(context.Background(), &User{
		ExternalID: "sub-9", Email: "u@x.com", Status: UserStatusActive,
	})
	require.NoError(t, err)
	token, hash, err := GenerateSessionToken()
	require.NoError(t, err)
	require.NoError(t, sessions.Create(context.Background(), Session{
		ID: hash, UserID: u.ID,
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))
	require.NoError(t, users.MarkDisabled(context.Background(), []string{u.ID}))

	h := a.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUserFromContextAbsent(t *testing.T) {
	_, ok := UserFromContext(context.Background())
	require.False(t, ok)
}

func TestLoginRedirectToIsSanitized(t *testing.T) {
	a, _, _, states, _ := newTestAuth(t)

	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet,
		"/auth/login?redirect_to=https://evil.example.com/steal", nil))
	require.Equal(t, http.StatusFound, rec.Code)

	for _, ls := range states.data {
		require.True(t, strings.HasPrefix(ls.RedirectTo, "/"),
			"绝不能把站外地址当作登录后跳转目标，否则是开放重定向")
		require.NotContains(t, ls.RedirectTo, "evil.example.com")
	}
}

func TestLoginRedirectToRejectsProtocolRelativeURL(t *testing.T) {
	// "https://evil.example.com/steal" 走的是 sanitizeRedirect 里
	// "不以 / 开头" 这条分支；"//evil.example.com" 本身是以 / 开头的，
	// 必须靠单独的 "//" 前缀检查才能拦住——这是开放重定向里最容易漏掉的一种，
	// 用独立的测试锁定，不能只靠上面那条覆盖。
	a, _, _, states, _ := newTestAuth(t)

	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet,
		"/auth/login?redirect_to=//evil.example.com/steal", nil))
	require.Equal(t, http.StatusFound, rec.Code)

	for _, ls := range states.data {
		require.Equal(t, "/", ls.RedirectTo,
			"协议相对 URL（// 开头）同样是开放重定向，必须被拦截为默认的 /")
	}
}

func TestLoginRedirectToRejectsBackslashVariant(t *testing.T) {
	// 浏览器解析 http/https 相对引用时把反斜杠当正斜杠处理，
	// 所以 "/\evil.example.com" 会被解析成跳到 evil.example.com——
	// 只查 "//" 前缀挡不住这个变体，是开放重定向过滤器最经典的绕过手法之一。
	a, _, _, states, _ := newTestAuth(t)

	rec := httptest.NewRecorder()
	a.HandleLogin(rec, httptest.NewRequest(http.MethodGet,
		`/auth/login?redirect_to=/\evil.example.com/steal`, nil))
	require.Equal(t, http.StatusFound, rec.Code)

	for _, ls := range states.data {
		require.Equal(t, "/", ls.RedirectTo,
			"反斜杠变体的协议相对 URL 同样是开放重定向，必须被拦截为默认的 /")
	}
}
