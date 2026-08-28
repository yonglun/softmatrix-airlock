package edge

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/softmatrix/airlock/internal/openai"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
)

// upstreamProvider 是记账时标注的供应商名。
// P1.1 阶段所有流量都经 LiteLLM，故统一标为 "litellm"；
// P2 起 Edge 会知道真实供应商，届时改为从模型目录解析。
const upstreamProvider = "litellm"

// Proxy 把请求透明转发到上游 LiteLLM，并记录用量与成本。
type Proxy struct {
	upstreamBaseURL string
	prices          pricing.Table
	usage           usage.Writer
	client          *http.Client
}

func NewProxy(upstreamBaseURL string, prices pricing.Table, w usage.Writer) *Proxy {
	return &Proxy{
		upstreamBaseURL: upstreamBaseURL,
		prices:          prices,
		usage:           w,
		client: &http.Client{
			// 不设总超时：长文本生成可能持续数分钟，由客户端与上游自行决定。
			Timeout: 0,
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := KeyFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusInternalServerError, "internal_error", "上下文中缺少密钥，鉴权中间件未生效")
		return
	}

	requestID := uuid.NewString()
	w.Header().Set("X-Airlock-Request-Id", requestID)

	rec := usage.Record{
		RequestID: requestID,
		Timestamp: time.Now(),
		OrgID:     key.OrgID,
		UserID:    key.UserID,
		KeyID:     key.ID,
		Provider:  upstreamProvider,
	}

	start := time.Now()
	defer func() {
		rec.LatencyMS = int(time.Since(start).Milliseconds())
		p.usage.Write(rec)
	}()

	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		rec.StatusCode = http.StatusBadRequest
		rec.ErrorType = "read_request_body_failed"
		writeAuthError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败")
		return
	}
	rec.Stream = isStreamRequest(reqBody)

	// 流式请求需要注入 include_usage，否则上游不会回传 usage。
	forwardBody := reqBody
	stripUsageChunk := false
	if rec.Stream {
		injectedBody, injected, err := ensureIncludeUsage(reqBody)
		if err != nil {
			rec.StatusCode = http.StatusBadRequest
			rec.ErrorType = "invalid_request_body"
			writeAuthError(w, http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON")
			return
		}
		forwardBody = injectedBody
		stripUsageChunk = injected
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method,
		p.upstreamBaseURL+r.URL.Path, bytes.NewReader(forwardBody))
	if err != nil {
		rec.StatusCode = http.StatusInternalServerError
		rec.ErrorType = "build_request_failed"
		writeAuthError(w, http.StatusInternalServerError, "internal_error", "构造上游请求失败")
		return
	}
	copyRequestHeaders(upstreamReq.Header, r.Header)
	upstreamReq.Header.Set("Authorization", "Bearer "+key.UpstreamKey)
	upstreamReq.ContentLength = int64(len(forwardBody))

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		rec.StatusCode = http.StatusBadGateway
		rec.ErrorType = "upstream_unreachable"
		writeAuthError(w, http.StatusBadGateway, "upstream_unreachable", "无法连接上游服务")
		return
	}
	defer resp.Body.Close()

	rec.StatusCode = resp.StatusCode
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		rec.ErrorType = "upstream_error"
		_, _ = io.Copy(w, resp.Body)
		return
	}

	if rec.Stream {
		outcome := pipeStream(w, resp.Body, stripUsageChunk, start)
		rec.TTFTMS = int(outcome.ttft.Milliseconds())
		rec.Model = outcome.model
		if outcome.usage == nil {
			rec.ErrorType = "usage_missing"
			return
		}
		rec.Usage = toPricingUsage(*outcome.usage)
		rec.CostMicro = p.cost(&rec)
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		rec.ErrorType = "read_response_failed"
		return
	}
	if _, err := w.Write(respBody); err != nil {
		slog.Warn("向客户端写响应失败", "request_id", requestID, "err", err)
	}

	protoUsage, model, err := openai.ExtractUsage(respBody)
	if err != nil {
		rec.ErrorType = "usage_parse_failed"
		return
	}
	rec.Model = model
	rec.Usage = toPricingUsage(protoUsage)
	rec.CostMicro = p.cost(&rec)
}

// cost 计算成本。任何失败都只记录 ErrorType 并返回 0，绝不影响客户拿到响应。
func (p *Proxy) cost(rec *usage.Record) pricing.Micro {
	price, err := p.prices.Lookup(rec.Provider, rec.Model, rec.Timestamp)
	if err != nil {
		rec.ErrorType = "price_not_found"
		slog.Warn("未找到生效价格", "provider", rec.Provider, "model", rec.Model, "err", err)
		return 0
	}
	cost, err := price.Cost(rec.Usage)
	if err != nil {
		rec.ErrorType = "cost_calc_failed"
		slog.Warn("成本计算失败", "model", rec.Model, "err", err)
		return 0
	}
	return cost
}

func toPricingUsage(u openai.Usage) pricing.Usage {
	return pricing.Usage{
		InputTokens:       u.PromptTokens,
		CachedInputTokens: u.CachedTokens,
		OutputTokens:      u.CompletionTokens,
		ReasoningTokens:   u.ReasoningTokens,
	}
}

// isStreamRequest 判断请求体是否要求流式响应。
func isStreamRequest(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
}

// hopByHopHeaders 不应被代理转发。
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		// Authorization 由调用方随后覆盖为上游密钥，这里先不拷贝。
		if http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
