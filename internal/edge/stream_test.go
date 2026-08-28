package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/stretchr/testify/require"
)

func TestEnsureIncludeUsageInjectsWhenAbsent(t *testing.T) {
	body, injected, err := ensureIncludeUsage([]byte(`{"model":"m","stream":true}`))
	require.NoError(t, err)
	require.True(t, injected)
	require.Contains(t, string(body), `"include_usage":true`)
}

func TestEnsureIncludeUsageLeavesExistingOptionAlone(t *testing.T) {
	original := `{"model":"m","stream":true,"stream_options":{"include_usage":true}}`
	body, injected, err := ensureIncludeUsage([]byte(original))
	require.NoError(t, err)
	require.False(t, injected, "客户端自己要了 usage，不能算作我们注入")
	require.JSONEq(t, original, string(body))
}

func TestEnsureIncludeUsageRespectsExplicitFalse(t *testing.T) {
	original := `{"model":"m","stream":true,"stream_options":{"include_usage":false}}`
	body, injected, err := ensureIncludeUsage([]byte(original))
	require.NoError(t, err)
	require.False(t, injected, "客户端显式关掉了，我们不覆盖")
	require.JSONEq(t, original, string(body))
}

// sseUpstream 构造一个吐固定 SSE 帧的上游。
// 每帧前故意停 2ms：否则本机回环太快，TTFT 取整到毫秒会是 0，
// 断言「首字延迟被测出来」就会随机失败。
func sseUpstream(frames ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			time.Sleep(2 * time.Millisecond)
			_, _ = w.Write([]byte(f + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func TestStreamForwardsContentAndCapturesUsage(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"你"}}]}`,
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"好"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	out := rec.Body.String()
	require.Contains(t, out, `"content":"你"`)
	require.Contains(t, out, `"content":"好"`)
	require.Contains(t, out, "[DONE]")

	records := w.get()
	require.Len(t, records, 1)
	require.True(t, records[0].Stream)
	require.Equal(t, int64(1_000_000), records[0].Usage.InputTokens)
	require.Equal(t, pricing.Micro(10_000_000), records[0].CostMicro)
	require.Positive(t, records[0].TTFTMS, "首字延迟必须被测出来")
}

func TestStreamStripsInjectedUsageChunk(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	// 客户端没有要 usage，Edge 自行注入，因此那一块必须被剥掉
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.NotContains(t, rec.Body.String(), `"usage"`,
		"Edge 自行注入 include_usage 时，usage 块不得下发给客户端")
	require.Contains(t, rec.Body.String(), "[DONE]")
}

func TestStreamKeepsUsageChunkWhenClientAskedForIt(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true,"stream_options":{"include_usage":true}}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Contains(t, rec.Body.String(), `"usage"`,
		"客户端自己要了 usage，必须原样下发")
}

func TestStreamRecordsWhenUpstreamSendsNoUsage(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	p.ServeHTTP(httptest.NewRecorder(), req)

	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, "usage_missing", records[0].ErrorType)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}
