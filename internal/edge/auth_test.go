package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/stretchr/testify/require"
)

func storeWith(t *testing.T) (*apikey.MemoryStore, string) {
	t.Helper()
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)

	s := apikey.NewMemoryStore(nil)
	s.Put(hash, &apikey.Key{
		ID: "k1", Prefix: prefix, OrgID: "org1", UserID: "user1",
		UpstreamKey: "sk-upstream", Status: apikey.StatusActive,
	})
	return s, plain
}

// echoKey 是被中间件包裹的处理器，把上下文里的密钥写回响应，便于断言。
func echoKey(w http.ResponseWriter, r *http.Request) {
	k, ok := KeyFromContext(r.Context())
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(k.OrgID + "/" + k.UserID))
}

func TestAuthAcceptsValidKey(t *testing.T) {
	store, plain := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "org1/user1", rec.Body.String())
}

func TestAuthRejectsMissingHeader(t *testing.T) {
	store, _ := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "missing_api_key")
}

func TestAuthRejectsNonBearerScheme(t *testing.T) {
	store, plain := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Basic "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthRejectsMalformedKeyWithoutHittingStore(t *testing.T) {
	h := Authenticate(apikey.NewMemoryStore(nil))(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong-prefix")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_api_key")
}

func TestAuthRejectsUnknownKey(t *testing.T) {
	other, _, _, err := apikey.Generate()
	require.NoError(t, err)

	store, _ := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthRejectsRevokedKey(t *testing.T) {
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, Status: apikey.StatusRevoked})

	h := Authenticate(store)(http.HandlerFunc(echoKey))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "key_revoked")
}

func TestAuthRejectsExpiredKey(t *testing.T) {
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, Status: apikey.StatusActive, ExpiresAt: &past})

	h := Authenticate(store)(http.HandlerFunc(echoKey))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "key_expired")
}

func TestKeyFromContextAbsent(t *testing.T) {
	_, ok := KeyFromContext(context.Background())
	require.False(t, ok)
}
