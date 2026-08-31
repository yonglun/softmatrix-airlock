package litellm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

func TestListTeamsHandlesNullOrganizationID(t *testing.T) {
	// 无组织的 Team 是合法的（根节点被标记为 key holder 时就会出现）。
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/team/list", r.URL.Path)
		_, _ = w.Write([]byte(`[
			{"team_id":"t1","team_alias":"数据面小组","organization_id":"o1"},
			{"team_id":"t2","team_alias":"独立组","organization_id":null}
		]`))
	})

	got, err := c.ListTeams(context.Background())
	require.NoError(t, err)
	require.Equal(t, []Team{
		{ID: "t1", Alias: "数据面小组", OrganizationID: strp("o1")},
		{ID: "t2", Alias: "独立组", OrganizationID: nil},
	}, got)
}

func TestCreateTeamSendsOurOwnIDAndOrg(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/team/new", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.CreateTeam(context.Background(),
		Team{ID: "node-uuid", Alias: "数据面小组", OrganizationID: strp("o1")}))
	require.Equal(t, "node-uuid", body["team_id"])
	require.Equal(t, "数据面小组", body["team_alias"])
	require.Equal(t, "o1", body["organization_id"])
}

func TestCreateTeamOmitsNullOrganization(t *testing.T) {
	// organization_id 为 nil 时必须发 null 或不发，不能发空字符串——
	// 上游对不存在的 organization_id 会 400 拒绝，空串会被当成不存在的组织。
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.CreateTeam(context.Background(),
		Team{ID: "t1", Alias: "独立组", OrganizationID: nil}))
	require.NotEqual(t, "", body["organization_id"], "不能发空字符串")
}

func TestUpdateTeamCanRehomeOrganization(t *testing.T) {
	// 跨子树移动叶子节点靠的就是这条：Team 可原地改挂，team_id 不变，
	// 因此绑在上面的 Key 不受影响。已实测确认。
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/team/update", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.UpdateTeam(context.Background(),
		Team{ID: "t1", Alias: "数据面小组", OrganizationID: strp("o2")}))
	require.Equal(t, "t1", body["team_id"])
	require.Equal(t, "o2", body["organization_id"])
}

func TestDeleteTeamSendsIDList(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/team/delete", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{}`))
	})

	require.NoError(t, c.DeleteTeam(context.Background(), "t1"))
	require.Equal(t, []any{"t1"}, body["team_ids"])
}

func TestCreateTeamPropagatesAPIError(t *testing.T) {
	// 挂到不存在的 organization_id 时上游返回 400——对账靠这个信号跳过。
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Organization not found"}}`))
	})

	err := c.CreateTeam(context.Background(), Team{ID: "t1", OrganizationID: strp("nope")})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.Status)
}
