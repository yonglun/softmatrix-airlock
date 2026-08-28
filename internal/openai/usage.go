// Package openai 解析 OpenAI 兼容协议的报文。
// 本包不知道 Airlock 的任何概念，只做协议层的读取。
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Usage 是协议层原样的 token 用量。
// 注意：CachedTokens 是 PromptTokens 的子集，ReasoningTokens 是 CompletionTokens 的子集。
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
}

type wireResponse struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// ExtractUsage 从一个完整的（非流式）响应体中提取 usage 与模型名。
// 响应中没有 usage 字段时返回零值，不视为错误——有些错误响应就是这样。
func ExtractUsage(body []byte) (Usage, string, error) {
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return Usage{}, "", fmt.Errorf("解析响应体失败: %w", err)
	}
	if wire.Usage == nil {
		return Usage{}, wire.Model, nil
	}
	u := Usage{
		PromptTokens:     wire.Usage.PromptTokens,
		CompletionTokens: wire.Usage.CompletionTokens,
	}
	if d := wire.Usage.PromptTokensDetails; d != nil {
		u.CachedTokens = d.CachedTokens
	}
	if d := wire.Usage.CompletionTokensDetails; d != nil {
		u.ReasoningTokens = d.ReasoningTokens
	}
	return u, wire.Model, nil
}

var (
	ssePrefix = []byte("data:")
	doneMark  = []byte("[DONE]")
)

// ParseSSEData 从一行 SSE 文本中取出 data 负载。
// 非 data 行（空行、event:、注释）返回 ok=false。
func ParseSSEData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, ssePrefix) {
		return nil, false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, ssePrefix))
	if len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

// IsDone 判断 SSE 负载是否为结束标记。
func IsDone(payload []byte) bool {
	return bytes.Equal(payload, doneMark)
}

var jsonNull = []byte("null")

// HasUsage 判断这块 SSE 负载是否携带非空的 usage 字段，
// 不要求 choices 为空——某些非 OpenAI 标准实现会把 usage 和内容塞进同一块。
// "usage":null 视为没带：不少兼容实现会在中间帧里显式写 null，只在末帧填真实值。
func HasUsage(payload []byte) bool {
	var probe struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	trimmed := bytes.TrimSpace(probe.Usage)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, jsonNull)
}

// IsUsageOnlyChunk 判断这是否是只携带 usage、不含任何内容的块。
// 请求中带 stream_options.include_usage=true 时，上游会在末尾多发这样一块。
// 若该选项是 Edge 自行注入的，这一块需要在转发给客户端前剥掉。
//
// 不能只看 choices 是否为空数组——实测部分 OpenAI 兼容实现（例如某些
// Azure AI Foundry 部署）会在这一块里塞一个 delta 为空对象、且没有
// finish_reason 的占位 choice，而不是把 choices 留空。这块依然不含任何
// 客户端关心的内容，所以也要算作 usage-only，否则注入的 usage 就会泄漏给客户端。
func IsUsageOnlyChunk(payload []byte) bool {
	var probe struct {
		Choices []struct {
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	trimmedUsage := bytes.TrimSpace(probe.Usage)
	if len(trimmedUsage) == 0 || bytes.Equal(trimmedUsage, jsonNull) {
		return false
	}
	for _, c := range probe.Choices {
		if c.FinishReason != nil {
			return false
		}
		if !isEmptyJSONObject(c.Delta) {
			return false
		}
	}
	return true
}

// isEmptyJSONObject 判断一段 JSON 是否是空对象（{}）或缺省值。
func isEmptyJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, jsonNull) {
		return true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return false
	}
	return len(m) == 0
}
