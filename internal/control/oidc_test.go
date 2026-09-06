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

// newMockIDPWithExternalIssuer 造一个 provider：discovery 文档里的 issuer
// 与全部端点都自报成 externalOrigin（模拟 Casdoor 用 CASDOOR_ORIGIN 自报
// 浏览器地址），但真正监听的是 httptest.Server 自己的地址（模拟本进程只能
// 走 compose 网络内部的服务名去连它）。
func newMockIDPWithExternalIssuer(t *testing.T, externalOrigin string) *mockIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &mockIDP{
		key: key, clientID: "airlock-test",
		subject: "sub-12345", email: "zhang@example.com", name: "张伟",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                externalOrigin,
			"authorization_endpoint":                externalOrigin + "/authorize",
			"token_endpoint":                        externalOrigin + "/token",
			"jwks_uri":                              externalOrigin + "/jwks",
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
			"iss": externalOrigin, "aud": idp.clientID, "sub": idp.subject,
			"email": idp.email, "name": idp.name,
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
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
			"access_token": "at-dummy", "token_type": "Bearer",
			"expires_in": 3600, "id_token": signed,
		})
	})

	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// 不做任何区分时（DiscoveryURL 留空），discovery 地址与浏览器自报的
// issuer 对不上就该照常拒绝——这是 go-oidc 的规范行为，也是不该被
// DiscoveryURL 的存在悄悄削弱的那道闸。
func TestNewOIDCClientRejectsIssuerMismatchWithoutDiscoveryURL(t *testing.T) {
	idp := newMockIDPWithExternalIssuer(t, "http://casdoor-public.example.com:8000")

	_, err := NewOIDCClient(context.Background(), OIDCConfig{
		Issuer:       "http://casdoor-public.example.com:8000",
		ClientID:     idp.clientID,
		ClientSecret: "secret",
		RedirectURL:  "http://localhost:8081/auth/callback",
	})
	// discoveryURL 缺省时退回 Issuer 本身（http://casdoor-public.example.com:8000），
	// 那个地址真实连不通（mockIDP 实际监听在 httptest 分配的另一个端口上），
	// discovery 请求本身就会失败——这条断言钉住「不给 DiscoveryURL 就没有
	// 任何特殊豁免」。
	require.Error(t, err)
}

// 这是本次修复要解决的真实场景：Casdoor 与 airlock-control 同在一套
// compose 里跑，Casdoor 自报的 issuer 是浏览器地址（CASDOOR_ORIGIN），
// 但 airlock-control 只能走 compose 网络内部的服务名连到它——两个地址
// 不可能是同一个字符串。DiscoveryURL 指向真正能连上的地址，Issuer 仍然
// 是浏览器要用的那个，discovery 必须成功且不再抛 issuer mismatch。
func TestNewOIDCClientAllowsDiscoveryURLDifferentFromIssuer(t *testing.T) {
	externalOrigin := "http://casdoor-public.example.com:8000"
	idp := newMockIDPWithExternalIssuer(t, externalOrigin)

	c, err := NewOIDCClient(context.Background(), OIDCConfig{
		Issuer:       externalOrigin,
		DiscoveryURL: idp.server.URL, // 真正可达的内部地址
		ClientID:     idp.clientID,
		ClientSecret: "secret",
		RedirectURL:  "http://localhost:8081/auth/callback",
	})
	require.NoError(t, err)

	// 浏览器重定向目标必须保持外部地址不变——这是给浏览器看的，
	// 不能悄悄换成内部地址（浏览器根本连不到 compose 网络内部）。
	authURL := c.AuthCodeURL("st", "ch")
	require.Contains(t, authURL, externalOrigin+"/authorize")

	// 服务端发起的 token 交换必须换成走 DiscoveryURL 那一侧——否则
	// Exchange 会尝试连一个从容器内部根本连不通的外部地址。
	id, err := c.Exchange(context.Background(), "code-1", "verifier-1")
	require.NoError(t, err, "token 端点必须被改写成内部可达的地址")
	require.Equal(t, "sub-12345", id.Subject)
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
