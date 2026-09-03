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
	return NewKeyAPI(iss, keys, orgs, authz.NewResolver(rbac)), orgs, admin, keys, rbac, uid
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

// rotateReq 发一次轮换请求。
func rotateReq(
	t *testing.T, api *KeyAPI, callerID, keyID, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/keys/"+keyID+"/rotate", strings.NewReader(body))
	req.SetPathValue("id", keyID)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: callerID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleRotate(rec, req)
	return rec
}

func TestRotateAPIByOwnerReturnsNewPlaintext(t *testing.T) {
	// 责任人可以自助轮换自己的密钥，与 P1.3b 的自助领取一脉相承。
	api, _, _, _, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	require.Equal(t, http.StatusCreated, issued.Code)
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	rec := rotateReq(t, api, uid, created.ID, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, created.ID, got.ID, "密钥身份跨轮换稳定")
	require.True(t, strings.HasPrefix(got.Key, "ak-"))
	require.NotEqual(t, created.Key, got.Key)
}

func TestRotateAPIByStrangerIs403(t *testing.T) {
	// 既不是责任人、也没有 key:write，不能轮换别人的密钥。
	api, _, _, _, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	rec := rotateReq(t, api, "someone-else", created.ID, `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_denied")
}

func TestRotateAPIByAdminWithKeyWrite(t *testing.T) {
	// 管理员可以替人处置。
	api, _, _, _, rbac, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	adminID := "admin-user"
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-admin", UserID: adminID, RoleID: authz.RoleOrgAdmin, OrgID: strp("gw"),
	}))

	rec := rotateReq(t, api, adminID, created.ID, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRotateAPIUnknownKeyIs404(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)
	rec := rotateReq(t, api, uid, "nope", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRotateAPIRevokedKeyIs409(t *testing.T) {
	api, _, _, keys, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))
	require.NoError(t, keys.Revoke(context.Background(), created.ID))

	rec := rotateReq(t, api, uid, created.ID, `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "key_not_active")
}

func TestRotateAPIWindowTooLongIs400(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	rec := rotateReq(t, api, uid, created.ID, `{"window_seconds":99999999}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "window_too_long")
}

func revokeOrgReq(t *testing.T, api *KeyAPI, callerID, orgID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/"+orgID+"/keys/revoke", nil)
	req.SetPathValue("id", orgID)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: callerID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleRevokeOrg(rec, req)
	return rec
}

func revokeAllReq(t *testing.T, api *KeyAPI, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys/revoke-all",
		strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: "someone", Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleRevokeAll(rec, req)
	return rec
}

func TestRevokeOrgAPIRevokesSubtree(t *testing.T) {
	api, _, _, keys, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	rec := revokeOrgReq(t, api, uid, "gw")
	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		Revoked int64 `json:"revoked"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, int64(1), got.Revoked)

	k, err := keys.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", k.Status)
}

func TestRevokeOrgAPIUnknownOrgIs404(t *testing.T) {
	api, _, _, _, _, uid := keyAPIFixture(t)
	rec := revokeOrgReq(t, api, uid, "nope")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRevokeAllAPIRequiresConfirmString(t *testing.T) {
	// 不可逆操作靠事前防护：确认字符串必须精确匹配。
	api, _, _, _, _, _ := keyAPIFixture(t)

	require.Equal(t, http.StatusBadRequest, revokeAllReq(t, api, `{}`).Code)

	rec := revokeAllReq(t, api, `{"confirm":"revoke all keys"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "大小写不同也必须拒绝")
	require.Contains(t, rec.Body.String(), "confirm_required")
}

func TestRevokeAllAPIWithConfirmRevokesEverything(t *testing.T) {
	api, _, _, keys, _, uid := keyAPIFixture(t)
	issued := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"x","models":[]}`)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &created))

	rec := revokeAllReq(t, api, `{"confirm":"REVOKE ALL KEYS"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	k, err := keys.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", k.Status)
}

func TestKeyViewCarriesRotationState(t *testing.T) {
	// 轮换完界面要说得出「旧凭据还能用多久」——共存窗口的存在意义
	// 就是给客户端替换凭据的时间，而这段时间还剩多少是唯一需要被看见的信息。
	api, _, _, _, _, uid := keyAPIFixture(t)
	rec := postKey(t, api, `{"org_id":"gw","user_id":"`+uid+`","name":"测试","models":[]}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created struct {
		ID               string  `json:"id"`
		RotatedAt        *string `json:"rotated_at"`
		PrevKeyExpiresAt *string `json:"prev_key_expires_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Nil(t, created.RotatedAt, "没轮换过的密钥这两个字段是空的")
	require.Nil(t, created.PrevKeyExpiresAt)

	rot := rotateReq(t, api, uid, created.ID, `{"window_seconds":3600}`)
	require.Equal(t, http.StatusOK, rot.Code)

	var rotated struct {
		RotatedAt        *string `json:"rotated_at"`
		PrevKeyExpiresAt *string `json:"prev_key_expires_at"`
	}
	require.NoError(t, json.Unmarshal(rot.Body.Bytes(), &rotated))
	require.NotNil(t, rotated.RotatedAt, "轮换后必须能看到轮换时间")
	require.NotNil(t, rotated.PrevKeyExpiresAt, "以及旧凭据的到期时间")
}

// listReq 发一次列表请求；subtree 为 true 时带上 ?subtree=true。
func listReq(t *testing.T, api *KeyAPI, orgID string, subtree bool) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/orgs/" + orgID + "/keys"
	if subtree {
		url += "?subtree=true"
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("id", orgID)
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)
	return rec
}

func TestListAPIDefaultIsSingleNodeAndSubtreeIsOptIn(t *testing.T) {
	// 不用 keyAPIFixture：它不返回 db，而子树匹配查的是真实
	// organizations 表，必须能往里插子节点。
	iss, orgs, _, keys, db, uid := issuerFixture(t)
	rbac := newFakeRBACStore()
	rbac.setPath("gw", "/gw")
	api := NewKeyAPI(iss, keys, orgs, authz.NewResolver(rbac))

	// issuerFixture 已经在真实库与 fakeOrgStore 里都建好了 gw（path=/gw）。
	seedOrg(t, db, "sub", "/gw/sub")
	seedKey(t, db, "k-gw", "h-gw", "gw", uid, "active")
	seedKey(t, db, "k-sub", "h-sub", "sub", uid, "active")

	rec := listReq(t, api, "gw", false)
	require.Equal(t, http.StatusOK, rec.Code)
	var single []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &single))
	require.Len(t, single, 1, "缺省行为必须不变：只列本节点")
	require.Equal(t, "k-gw", single[0].ID)

	rec2 := listReq(t, api, "gw", true)
	require.Equal(t, http.StatusOK, rec2.Code)
	var sub []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &sub))
	require.Len(t, sub, 2, "带 subtree=true 时子节点的密钥也要在")
}
