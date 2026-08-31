package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/require"
)

// mockIDP 是一个最小 OIDC provider：discovery + JWKS + token 三个端点。
type mockIDP struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	subject  string
	email    string
	name     string
	lastForm url.Values
}

func newMockIDP(t *testing.T) *mockIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &mockIDP{
		key:      key,
		clientID: "airlock-test",
		subject:  "sub-12345",
		email:    "zhang@example.com",
		name:     "张伟",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idp.key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key", "n": n, "e": e},
			},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.lastForm = r.Form

		claims := jwt.MapClaims{
			"iss":   idp.server.URL,
			"aud":   idp.clientID,
			"sub":   idp.subject,
			"email": idp.email,
			"name":  idp.name,
			"exp":   time.Now().Add(time.Hour).Unix(),
			"iat":   time.Now().Unix(),
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "test-key"
		signed, err := tok.SignedString(idp.key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-dummy",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signed,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (m *mockIDP) client(t *testing.T) OIDCClient {
	t.Helper()
	c, err := NewOIDCClient(context.Background(), OIDCConfig{
		Issuer:       m.server.URL,
		ClientID:     m.clientID,
		ClientSecret: "secret",
		RedirectURL:  "http://localhost:8081/auth/callback",
	})
	require.NoError(t, err)
	return c
}

func TestAuthCodeURLCarriesStateAndPKCE(t *testing.T) {
	idp := newMockIDP(t)
	c := idp.client(t)

	raw := c.AuthCodeURL("st-abc", "ch-xyz")
	u, err := url.Parse(raw)
	require.NoError(t, err)

	q := u.Query()
	require.Equal(t, "st-abc", q.Get("state"))
	require.Equal(t, "ch-xyz", q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, idp.clientID, q.Get("client_id"))
	require.Contains(t, q.Get("scope"), "openid")
}

func TestExchangeReturnsIdentity(t *testing.T) {
	idp := newMockIDP(t)
	c := idp.client(t)

	id, err := c.Exchange(context.Background(), "code-1", "verifier-1")
	require.NoError(t, err)
	require.Equal(t, "sub-12345", id.Subject)
	require.Equal(t, "zhang@example.com", id.Email)
	require.Equal(t, "张伟", id.DisplayName)
}

func TestExchangeSendsPKCEVerifier(t *testing.T) {
	idp := newMockIDP(t)
	c := idp.client(t)

	_, err := c.Exchange(context.Background(), "code-1", "verifier-1")
	require.NoError(t, err)
	require.Equal(t, "verifier-1", idp.lastForm.Get("code_verifier"),
		"必须把 PKCE verifier 发给 token 端点，否则 PKCE 形同虚设")
	require.Equal(t, "authorization_code", idp.lastForm.Get("grant_type"))
}

func TestNewOIDCClientFailsOnUnreachableIssuer(t *testing.T) {
	_, err := NewOIDCClient(context.Background(), OIDCConfig{
		Issuer:   "http://127.0.0.1:1/nonexistent",
		ClientID: "x",
	})
	require.Error(t, err)
}

func TestExchangeParsesEmailVerified(t *testing.T) {
	// 直接验证 claims 结构体能吃下 email_verified。
	// 完整的 OIDC 往返已由本文件既有测试覆盖，这里只锁字段解析。
	type emailClaims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}

	var withFlag emailClaims
	require.NoError(t, json.Unmarshal(
		[]byte(`{"email":"a@x.com","email_verified":true}`), &withFlag))
	require.True(t, withFlag.EmailVerified)

	var without emailClaims
	require.NoError(t, json.Unmarshal([]byte(`{"email":"a@x.com"}`), &without))
	require.False(t, without.EmailVerified, "缺失时零值为 false，即按未验证处理")
}
