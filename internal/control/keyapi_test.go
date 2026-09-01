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
	// resolver.Can 靠 OrgPath 算祖先链，fake RBAC store 不会自动跟着
	// fakeOrgStore 的树走，必须显式登记——否则 HandleRevoke 的权限判定
	// 直接因 authz.ErrOrgNotFound 拿到 500，而不是被测的业务状态码。
	rbac.setPath("gw", "/gw")
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

// revokeAs 以一个持有全局 platform_admin 的用户身份调吊销。
// HandleRevoke 自己做权限判定，因此必须带上用户上下文与授予，
// 否则拿到的是 500（上下文缺用户）而不是被测的业务状态码。
func revokeAs(t *testing.T, api *KeyAPI, rbac *fakeRBACStore, id string) *httptest.ResponseRecorder {
	t.Helper()
	u := &User{ID: "u1", Status: UserStatusActive}
	_ = rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-key-" + id, UserID: "u1", RoleID: authz.RolePlatformAdmin,
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/keys/"+id, nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, u))
	rec := httptest.NewRecorder()
	api.HandleRevoke(rec, req)
	return rec
}

func TestRevokeAPI(t *testing.T) {
	api, _, _, keys, rbac, uid := keyAPIFixture(t)
	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":[]}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	require.Equal(t, http.StatusNoContent, revokeAs(t, api, rbac, created.ID).Code)

	stored, err := keys.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", stored.Status)
}

func TestRevokeAPIUnknownKeyIs404(t *testing.T) {
	api, _, _, _, rbac, _ := keyAPIFixture(t)
	require.Equal(t, http.StatusNotFound, revokeAs(t, api, rbac, "nope").Code)
}

func TestRevokeAPIWithoutPermissionIs403(t *testing.T) {
	// 判定下沉到处理器意味着它必须自己拦住无授予的调用者——
	// 中间件在这条路由上只校验了「已登录」。
	api, _, _, _, _, uid := keyAPIFixture(t)
	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":[]}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	// 不给任何授予。
	u := &User{ID: "nobody", Status: UserStatusActive}
	req := httptest.NewRequest(http.MethodDelete, "/api/keys/"+created.ID, nil)
	req.SetPathValue("id", created.ID)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, u))
	rec2 := httptest.NewRecorder()
	api.HandleRevoke(rec2, req)

	require.Equal(t, http.StatusForbidden, rec2.Code)
	require.Contains(t, rec2.Body.String(), "permission_denied")
}
