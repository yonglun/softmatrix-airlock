package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// keyAPIFixture 一次定死签名：orgs 供「非 key holder 节点」用例建节点，
// rbac 供 Task 11 的吊销用例造授予，uid 是沿路种下的真实用户
// （api_keys.user_id 上有 Task 2 才发现的既有外键，字面量会被拒绝）。
func keyAPIFixture(t *testing.T) (api *KeyAPI, orgs *fakeOrgStore, admin *fakeKeyAdmin, keys KeyStore, rbac *fakeRBACStore, uid string) {
	t.Helper()
	iss, orgs, admin, keys, _, uid := issuerFixture(t)
	rbac = newFakeRBACStore()
	return NewKeyAPI(iss, keys, authz.NewResolver(rbac)), orgs, admin, keys, rbac, uid
}

func postKey(t *testing.T, api *KeyAPI, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.HandleIssue(rec, req)
	return rec
}

func TestIssueAPIReturnsPlaintextOnce(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)

	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":["qwen-plus"]}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	var got struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEmpty(t, got.ID)
	require.True(t, strings.HasPrefix(got.Key, "ak-"), "明文只在这里返回一次")
}

func TestIssueAPIRequiresModelsField(t *testing.T) {
	// 上游把 models 缺失当成「放行全部模型」（fail-open）。
	// 只能在 API 边界堵：缺字段直接 400，而不是默默签出一把不受限的密钥。
	api, _, admin, _, _, uid := keyAPIFixture(t)

	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "models_required")
	require.Empty(t, admin.callsSnapshot(), "校验失败不该碰上游")
}

func TestIssueAPIAcceptsExplicitEmptyModels(t *testing.T) {
	// 显式的空数组是合法的「放行全部」，与「忘了传」必须区分开。
	api, _, _, _, _, uid := keyAPIFixture(t)

	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":[]}`)
	require.Equal(t, http.StatusCreated, rec.Code)
}

func TestIssueAPINonKeyHolderIs409(t *testing.T) {
	// 上游不校验 team 存在性，挂到不存在的 team 上会静默成功。
	// 这道闸门只能自己把，且要与「节点根本不存在」区分开。
	api, orgs, admin, _, _, uid := keyAPIFixture(t)
	require.NoError(t, orgs.Create(context.Background(),
		&Org{ID: "plain", Name: "普通部门"}))

	rec := postKey(t, api, `{"org_id":"plain","user_id":"`+uid+`","name":"x","models":[]}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "org_not_key_holder")
	require.Empty(t, admin.callsSnapshot(), "校验失败不该碰上游")
}

func TestIssueAPIUnknownOrgIs404(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)

	rec := postKey(t, api, `{"org_id":"nope","user_id":"`+uid+`","name":"x","models":[]}`)
	require.Equal(t, http.StatusNotFound, rec.Code, "节点不存在是 404，与「存在但不是密钥边界」不同")
}

func TestIssueAPIRejectsMalformedBody(t *testing.T) {
	api, _, _, _, _, _ := keyAPIFixture(t)

	rec := postKey(t, api, `{not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListKeysNeverLeaksSecrets(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)
	require.Equal(t, http.StatusCreated,
		postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":[]}`).Code)

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/gw/keys", nil)
	req.SetPathValue("id", "gw")
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "ak-", "展示前缀可以有")
	require.NotContains(t, body, "sk-", "上游密钥绝不能出现在列表里")
	require.NotContains(t, body, "upstream", "连字段名都不该暴露")
}

func TestListKeysEmptyIsArrayNotNull(t *testing.T) {
	api, _, _, _, _, _ := keyAPIFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/api/orgs/gw/keys", nil)
	req.SetPathValue("id", "gw")
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", rec.Body.String())
}
