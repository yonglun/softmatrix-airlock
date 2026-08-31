package litellm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListOrganizationsParsesBareArray(t *testing.T) {
	// 上游返回的是裸数组，不是 {"organizations":[...]}——已实测确认。
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/organization/list", r.URL.Path)
		_, _ = w.Write([]byte(`[
			{"organization_id":"o1","organization_alias":"研发中心","spend":0.0},
			{"organization_id":"o2","organization_alias":"销售部"}
		]`))
	})

	got, err := c.ListOrganizations(context.Background())
	require.NoError(t, err)
	require.Equal(t, []Organization{
		{ID: "o1", Alias: "研发中心"},
		{ID: "o2", Alias: "销售部"},
	}, got)
}

func TestListOrganizationsEmptyIsEmptySlice(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	got, err := c.ListOrganizations(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestCreateOrganizationSendsOurOwnID(t *testing.T) {
	// 自带 ID 是整个同步设计的基础：映射因此退化为恒等式，不需要映射表。
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/organization/new", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.CreateOrganization(context.Background(),
		Organization{ID: "node-uuid", Alias: "研发中心"}))
	require.Equal(t, "node-uuid", body["organization_id"])
	require.Equal(t, "研发中心", body["organization_alias"])
}

func TestUpdateOrganizationUsesPatch(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPatch, r.Method)
		require.Equal(t, "/organization/update", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.UpdateOrganization(context.Background(),
		Organization{ID: "o1", Alias: "技术中心"}))
	require.Equal(t, "o1", body["organization_id"])
	require.Equal(t, "技术中心", body["organization_alias"])
}

func TestDeleteOrganizationSendsIDList(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/organization/delete", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`[]`))
	})

	require.NoError(t, c.DeleteOrganization(context.Background(), "o1"))
	require.Equal(t, []any{"o1"}, body["organization_ids"])
}

func TestCreateOrganizationPropagatesAPIError(t *testing.T) {
	// 重复创建同一个 organization_id 时上游返回 500——这是实测到的真实行为，
	// 不是网络故障。调用方需要能拿到状态码自行判断。
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"Internal server error"}}`))
	})

	err := c.CreateOrganization(context.Background(), Organization{ID: "dup", Alias: "x"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusInternalServerError, apiErr.Status)
}
