package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractUsageFromNonStreamResponse(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-1",
		"model": "deepseek-chat",
		"choices": [{"message": {"content": "hi"}}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`)

	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "deepseek-chat", model)
	require.Equal(t, int64(100), u.PromptTokens)
	require.Equal(t, int64(50), u.CompletionTokens)
	require.Equal(t, int64(0), u.CachedTokens)
	require.Equal(t, int64(0), u.ReasoningTokens)
}

func TestExtractUsageReadsTokenDetails(t *testing.T) {
	body := []byte(`{
		"model": "qwen-plus",
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 400,
			"prompt_tokens_details": {"cached_tokens": 600},
			"completion_tokens_details": {"reasoning_tokens": 120}
		}
	}`)

	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "qwen-plus", model)
	require.Equal(t, int64(1000), u.PromptTokens)
	require.Equal(t, int64(600), u.CachedTokens)
	require.Equal(t, int64(400), u.CompletionTokens)
	require.Equal(t, int64(120), u.ReasoningTokens)
}

func TestExtractUsageWhenAbsentReturnsZero(t *testing.T) {
	body := []byte(`{"model": "gpt-4o-mini", "choices": []}`)
	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", model)
	require.Equal(t, Usage{}, u)
}

func TestExtractUsageFailsOnInvalidJSON(t *testing.T) {
	_, _, err := ExtractUsage([]byte(`{not json`))
	require.Error(t, err)
}

func TestParseSSEData(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantData string
		wantOK   bool
	}{
		{"标准数据行", `data: {"a":1}`, `{"a":1}`, true},
		{"无空格", `data:{"a":1}`, `{"a":1}`, true},
		{"结束标记", `data: [DONE]`, `[DONE]`, true},
		{"空行", ``, ``, false},
		{"事件行", `event: message`, ``, false},
		{"注释行", `: keep-alive`, ``, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, ok := ParseSSEData([]byte(tc.line))
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantData, string(data))
			}
		})
	}
}

func TestIsDone(t *testing.T) {
	require.True(t, IsDone([]byte("[DONE]")))
	require.False(t, IsDone([]byte(`{"a":1}`)))
}

func TestHasUsage(t *testing.T) {
	require.True(t, HasUsage([]byte(`{"choices":[],"usage":{"prompt_tokens":10}}`)))
	require.False(t, HasUsage([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`)),
		"没有 usage 字段")
	require.False(t, HasUsage([]byte(`{"choices":[{"delta":{"content":"hi"}}],"usage":null}`)),
		"usage 显式为 null 等同于没带——很多兼容实现在中间帧里就是这么写的")
	require.False(t, HasUsage([]byte(`{not json`)))
}

func TestIsUsageOnlyChunk(t *testing.T) {
	usageOnly := []byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	require.True(t, IsUsageOnlyChunk(usageOnly))

	contentChunk := []byte(`{"choices":[{"delta":{"content":"hi"}}]}`)
	require.False(t, IsUsageOnlyChunk(contentChunk))

	contentWithUsage := []byte(`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10}}`)
	require.False(t, IsUsageOnlyChunk(contentWithUsage), "带内容的块不算 usage-only")

	// 实测 gpt-5.6-luna（Azure AI Foundry）不会把 choices 留空，
	// 而是塞一个 delta 为空对象、无 finish_reason 的占位 choice。
	azureStyleUsageOnly := []byte(`{"choices":[{"index":0,"delta":{}}],"usage":{"prompt_tokens":9,"completion_tokens":40}}`)
	require.True(t, IsUsageOnlyChunk(azureStyleUsageOnly), "占位 choice（空 delta、无 finish_reason）也算 usage-only")

	finishReasonChunk := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	require.False(t, IsUsageOnlyChunk(finishReasonChunk), "带 finish_reason 的块不算 usage-only（无 usage 也不算）")

	usageWithFinishReason := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9}}`)
	require.False(t, IsUsageOnlyChunk(usageWithFinishReason), "finish_reason 块即便带 usage 也不算 usage-only")

	nullUsageChunk := []byte(`{"choices":[],"usage":null}`)
	require.False(t, IsUsageOnlyChunk(nullUsageChunk), "usage 显式为 null 不算 usage-only")
}
