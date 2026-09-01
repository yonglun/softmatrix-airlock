package litellm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func f64p(v float64) *float64 { return &v }
func intp(v int) *int         { return &v }

func TestGenerateKeySendsAllFields(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/key/generate", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{"key":"sk-airlock-own"}`))
	})

	err := c.GenerateKey(context.Background(), Key{
		Key: "sk-airlock-own", TeamID: strp("gw"),
		Models: []string{"qwen-plus"}, MaxBudget: f64p(10.5),
		BudgetDuration: strp("30d"), RPMLimit: intp(60), TPMLimit: intp(10000),
		Duration: strp("3600s"),
	})
	require.NoError(t, err)

	require.Equal(t, "sk-airlock-own", body["key"], "必须自带 key 值，这是幂等重试的基础")
	require.Equal(t, "gw", body["team_id"])
	require.Equal(t, []any{"qwen-plus"}, body["models"])
	require.Equal(t, 10.5, body["max_budget"])
	require.Equal(t, "30d", body["budget_duration"])
	require.Equal(t, float64(60), body["rpm_limit"])
	require.Equal(t, float64(10000), body["tpm_limit"])
	require.Equal(t, "3600s", body["duration"])
}

func TestGenerateKeyAlwaysSendsModelsEvenWhenNil(t *testing.T) {
	// 上游把 models 缺失/为空一律当成「放行全部模型」（fail-open）。
	// 客户端必须始终发出该字段且为数组而非 null，
	// 让「漏发」在这一层就不可能发生。
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.GenerateKey(context.Background(), Key{Key: "sk-x", Models: nil}))

	got, ok := body["models"]
	require.True(t, ok, "models 字段必须存在")
	require.Equal(t, []any{}, got, "nil 必须归一成空数组，不能发 null")
}

func TestGenerateKeyOmitsUnsetOptionalFields(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.GenerateKey(context.Background(),
		Key{Key: "sk-x", Models: []string{}}))

	for _, absent := range []string{"max_budget", "rpm_limit", "tpm_limit", "duration", "team_id"} {
		_, present := body[absent]
		require.False(t, present, "未设置的 %s 不该出现在请求里", absent)
	}
}

func TestGenerateKeyPropagatesAPIError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad"}}`))
	})

	err := c.GenerateKey(context.Background(), Key{Key: "sk-x", Models: []string{}})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.Status)
}
