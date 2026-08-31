package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/litellm"
)

func syncAPIFixture(t *testing.T) (*SyncAPI, *fakeLiteLLM) {
	t.Helper()
	store := newFakeOrgStore()
	seedTree(t, store)
	admin := newFakeLiteLLM()
	return NewSyncAPI(NewSyncer(SyncerDeps{Orgs: store, Admin: admin})), admin
}

func TestStatusReportsPendingWork(t *testing.T) {
	api, _ := syncAPIFixture(t)

	rec := httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/litellm/sync/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, true, body["enabled"])
	require.Len(t, body["missing_orgs"], 2)
	require.Len(t, body["missing_teams"], 1)
}

func TestStatusReportsExtraEntities(t *testing.T) {
	// 多出来的实体必须可见，否则「绝不删除」会退化成静默积累垃圾。
	api, admin := syncAPIFixture(t)
	admin.setOrg(litellm.Organization{ID: "stranger", Alias: "别人建的"})

	rec := httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/litellm/sync/status", nil))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, []any{"stranger"}, body["extra_orgs"])
}

func TestStatusWhenSyncDisabled(t *testing.T) {
	// 未配置 LITELLM_MASTER_KEY 时同步整体禁用，接口仍要能答，而不是 500。
	api := NewSyncAPI(nil)

	rec := httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/litellm/sync/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, false, body["enabled"])
}

func TestStatusUpstreamDownIsBadGateway(t *testing.T) {
	api, admin := syncAPIFixture(t)
	admin.listOrgsErr = errors.New("上游不可用")

	rec := httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/litellm/sync/status", nil))
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "litellm_unreachable")
}

func TestTriggerRunsReconcileAndReportsResult(t *testing.T) {
	api, admin := syncAPIFixture(t)

	rec := httptest.NewRecorder()
	api.HandleTrigger(rec, httptest.NewRequest(http.MethodPost, "/api/litellm/sync", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, float64(2), body["orgs_created"])
	require.True(t, admin.hasOrg("rd"))
}

func TestTriggerWhenSyncDisabledIs503(t *testing.T) {
	api := NewSyncAPI(nil)

	rec := httptest.NewRecorder()
	api.HandleTrigger(rec, httptest.NewRequest(http.MethodPost, "/api/litellm/sync", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "sync_disabled")
}

func TestTriggerUpstreamDownIsBadGateway(t *testing.T) {
	api, admin := syncAPIFixture(t)
	admin.listOrgsErr = errors.New("上游不可用")

	rec := httptest.NewRecorder()
	api.HandleTrigger(rec, httptest.NewRequest(http.MethodPost, "/api/litellm/sync", nil))
	require.Equal(t, http.StatusBadGateway, rec.Code)
}
