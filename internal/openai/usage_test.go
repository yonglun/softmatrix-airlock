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

func TestIsUsageOnlyChunk(t *testing.T) {
	usageOnly := []byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	require.True(t, IsUsageOnlyChunk(usageOnly))

	contentChunk := []byte(`{"choices":[{"delta":{"content":"hi"}}]}`)
	require.False(t, IsUsageOnlyChunk(contentChunk))

	contentWithUsage := []byte(`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10}}`)
	require.False(t, IsUsageOnlyChunk(contentWithUsage), "带内容的块不算 usage-only")
}
