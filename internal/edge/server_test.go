package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/stretchr/testify/require"
)

func TestHealthzNeedsNoAuth(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestChatCompletionsRequiresAuth(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUnknownPathReturns404(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAllV1PathsAreProxied(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `","model":"deepseek-chat","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, OrgID: "o", UserID: "u",
		UpstreamKey: "sk-up", Status: apikey.StatusActive})

	srv := NewServer(Deps{
		Keys: store, Prices: testTable(), Usage: &recordingWriter{},
		UpstreamBaseURL: upstream.URL,
	})

	for _, path := range []string{"/v1/chat/completions", "/v1/embeddings", "/v1/models"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+plain)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "路径 %s 应被代理", path)
		require.Contains(t, rec.Body.String(), path)
	}
}
