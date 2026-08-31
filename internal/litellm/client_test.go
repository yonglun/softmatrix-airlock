package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, MasterKey: "sk-test-master", Timeout: 2 * time.Second})
}

func TestSendsBearerMasterKey(t *testing.T) {
	var got string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	})

	var out []Organization
	require.NoError(t, c.do(context.Background(), http.MethodGet, "/organization/list", nil, &out))
	require.Equal(t, "Bearer sk-test-master", got)
}

func TestErrorNeverLeaksMasterKey(t *testing.T) {
	// master key 是全系统权限最高的凭据。它绝不能出现在错误信息里——
	// 错误会进日志、进 HTTP 响应，一旦泄漏等于把上游网关拱手让人。
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	})

	err := c.do(context.Background(), http.MethodGet, "/organization/list", nil, nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sk-test-master")
}

func TestNon2xxBecomesAPIError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"already exists"}}`))
	})

	err := c.do(context.Background(), http.MethodGet, "/team/list", nil, nil)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.Status)
	require.Contains(t, apiErr.Body, "already exists")
}

func TestMalformedJSONIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	})

	var out []Organization
	require.Error(t, c.do(context.Background(), http.MethodGet, "/organization/list", nil, &out))
}

func TestTimeoutIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`[]`))
	})
	c.hc.Timeout = 20 * time.Millisecond

	var out []Organization
	err := c.do(context.Background(), http.MethodGet, "/organization/list", nil, &out)
	require.Error(t, err)
}

func TestBaseURLTrailingSlashIsTolerated(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL + "/", MasterKey: "k"})
	var out []Organization
	require.NoError(t, c.do(context.Background(), http.MethodGet, "/organization/list", nil, &out))
	require.Equal(t, "/organization/list", path)
	require.False(t, strings.Contains(path, "//"))
}
