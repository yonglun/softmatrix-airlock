package edge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
	"github.com/stretchr/testify/require"
)

type recordingWriter struct {
	mu      sync.Mutex
	records []usage.Record
}

func (w *recordingWriter) Write(r usage.Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, r)
}

func (w *recordingWriter) get() []usage.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]usage.Record, len(w.records))
	copy(out, w.records)
	return out
}

func testTable() pricing.Table {
	tbl, err := pricing.NewMemoryTable([]pricing.ModelPrice{{
		Provider:      "litellm",
		Model:         "deepseek-chat",
		EffectiveFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []pricing.Tier{{
			MaxInputTokens:   0,
			InputPer1M:       2_000_000,
			CachedInputPer1M: 500_000,
			OutputPer1M:      8_000_000,
			ReasoningPer1M:   8_000_000,
		}},
	}})
	if err != nil {
		panic(err) // 测试数据固定合法，出错说明测试本身写错了
	}
	return tbl
}

func testKey() *apikey.Key {
	return &apikey.Key{ID: "k1", OrgID: "org1", UserID: "user1",
		UpstreamKey: "sk-upstream", Status: apikey.StatusActive}
}

// withKey 把密钥塞进请求上下文，模拟鉴权中间件已经跑过。
func withKey(r *http.Request, k *apikey.Key) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), keyCtxKey, k))
}

func TestProxyForwardsAndSwapsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","usage":{"prompt_tokens":100,"completion_tokens":50}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Bearer sk-upstream", gotAuth, "必须换成上游密钥，不能透传 ak-")
	require.JSONEq(t, body, gotBody, "请求体必须原样转发")
}

func TestProxyRecordsUsageAndCost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"deepseek-chat",
			"usage":{
				"prompt_tokens":1000000,
				"completion_tokens":1000000,
				"prompt_tokens_details":{"cached_tokens":0},
				"completion_tokens_details":{"reasoning_tokens":0}
			}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	p.ServeHTTP(httptest.NewRecorder(), req)

	records := w.get()
	require.Len(t, records, 1)
	r := records[0]
	require.Equal(t, "org1", r.OrgID)
	require.Equal(t, "user1", r.UserID)
	require.Equal(t, "k1", r.KeyID)
	require.Equal(t, "deepseek-chat", r.Model)
	require.Equal(t, int64(1_000_000), r.Usage.InputTokens)
	require.Equal(t, int64(1_000_000), r.Usage.OutputTokens)
	// 100 万输入 * 2元/百万 + 100 万输出 * 8元/百万 = 10 元
	require.Equal(t, pricing.Micro(10_000_000), r.CostMicro)
	require.Equal(t, http.StatusOK, r.StatusCode)
	require.False(t, r.Stream)
	require.NotEmpty(t, r.RequestID)
}

func TestProxyReturnsUpstreamBodyUnchanged(t *testing.T) {
	payload := `{"model":"deepseek-chat","choices":[{"message":{"content":"你好"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.JSONEq(t, payload, rec.Body.String())
}

func TestProxyRecordsUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, http.StatusTooManyRequests, records[0].StatusCode)
	require.Equal(t, "upstream_error", records[0].ErrorType)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}

func TestProxyRecordsZeroCostWhenPriceMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"unknown-model","usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"unknown-model"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "缺价格不能影响客户拿到响应")
	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
	require.Equal(t, "price_not_found", records[0].ErrorType)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestProxyRecordsUsageOnBodyReadFailure(t *testing.T) {
	w := &recordingWriter{}
	p := NewProxy("http://unused", testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", errReader{}), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	records := w.get()
	require.Len(t, records, 1, "body 读取失败也必须留下一条用量/审计记录")
	require.Equal(t, http.StatusBadRequest, records[0].StatusCode)
	require.Equal(t, "read_request_body_failed", records[0].ErrorType)
}

func TestProxyFailsWithoutKeyInContext(t *testing.T) {
	p := NewProxy("http://unused", testTable(), &recordingWriter{})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestProxySetsRequestIDHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.NotEmpty(t, rec.Header().Get("X-Airlock-Request-Id"))
}

func TestIsStreamRequest(t *testing.T) {
	require.True(t, isStreamRequest([]byte(`{"stream":true}`)))
	require.False(t, isStreamRequest([]byte(`{"stream":false}`)))
	require.False(t, isStreamRequest([]byte(`{}`)))
	require.False(t, isStreamRequest([]byte(`not json`)))
}
