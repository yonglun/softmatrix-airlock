package edge

import (
	"bytes"
	"fmt"
	"log/slog"
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
	require.Equal(t, "usage_missing", records[0].ErrorType,
		"流干净地结束、只是上游从没带 usage，这才是 usage_missing")
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}

// brokenSSEUpstream 发送一个完整帧后直接砍断 TCP 连接（不写终止块），
// 模拟网络中断——区别于 sseUpstream 那种干净结束响应体的写法。
func brokenSSEUpstream(t *testing.T, frame string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		require.True(t, ok, "测试环境必须支持 Hijack")
		conn, buf, err := hj.Hijack()
		require.NoError(t, err)
		defer conn.Close()

		body := frame + "\n\n"
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = buf.WriteString("Content-Type: text/event-stream\r\n")
		_, _ = buf.WriteString("Transfer-Encoding: chunked\r\n\r\n")
		_, _ = fmt.Fprintf(buf, "%x\r\n%s\r\n", len(body), body)
		_ = buf.Flush()
		// 故意不写终止块（0\r\n\r\n），直接断线。
	}))
}

func TestStreamRecordsReadFailureOnBrokenConnection(t *testing.T) {
	upstream := brokenSSEUpstream(t,
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`)
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Contains(t, rec.Body.String(), `"content":"hi"`,
		"断线前客户端已经收到的部分内容必须已经转发出去")

	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, "stream_read_failed", records[0].ErrorType,
		"连接中途被砍断，必须和干净结束的 usage_missing 区分开")
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}

func TestStreamStillWarnsWhenConnectionBreaksAfterUsageCaptured(t *testing.T) {
	// usage 已经拿到了，账不受影响，但连接终归是不正常结束的——
	// 运维需要能从日志里看到这次“看似成功”的请求其实断线了。
	frame := `data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}` +
		"\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	upstream := brokenSSEUpstream(t, frame)
	defer upstream.Close()

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prevLogger)

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Airlock-Request-Id")
	require.NotEmpty(t, requestID)

	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, "", records[0].ErrorType,
		"usage 已捕获，不该因为随后断线而被标记成失败")
	require.Equal(t, int64(10), records[0].Usage.InputTokens)
	require.Positive(t, records[0].CostMicro)

	require.Contains(t, logBuf.String(), requestID,
		"即使 usage 已捕获，断线本身也必须留下告警日志供运维排查")
}

func TestStreamRecordsSSELineTooLong(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		huge := strings.Repeat("x", sseScanBufferMax+1024)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"` + huge + `"}}]}` + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	p.ServeHTTP(httptest.NewRecorder(), req)

	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, "sse_line_too_long", records[0].ErrorType,
		"单帧超过缓冲区上限必须能和普通读错误、usage_missing 区分开")
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}

func TestStreamIgnoresInterimNullUsageAndCapturesRealUsage(t *testing.T) {
	// 一些兼容实现在每个中间帧都显式写 "usage":null，只在末帧填真实值。
	// 早期版本一旦把第一个 null usage 当成"已经拿到"，真实的那个就再也捕获不到了。
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}],"usage":null}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
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
	require.NotEqual(t, "usage_missing", records[0].ErrorType)
	require.Equal(t, int64(10), records[0].Usage.InputTokens)
	require.Equal(t, int64(5), records[0].Usage.OutputTokens)
}

func TestStreamCapturesUsageEvenWhenBundledWithContent(t *testing.T) {
	// 非标准 OpenAI 兼容上游：把 usage 和内容塞进了同一块，
	// IsUsageOnlyChunk 会判定它不是 usage-only（choices 非空），
	// 但 usage 字段本身确实存在，不该被漏掉。
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
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
	require.NotEqual(t, "usage_missing", records[0].ErrorType)
	require.Equal(t, int64(10), records[0].Usage.InputTokens)
	require.Equal(t, int64(5), records[0].Usage.OutputTokens)
}
