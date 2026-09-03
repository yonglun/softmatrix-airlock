package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func requestAPIFixture(t *testing.T) (
	api *RequestAPI, svc *ApprovalService, requesterID, approverID, keyID string,
) {
	t.Helper()
	svc, db, _, requesterID, approverID, keyID := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	api = NewRequestAPI(svc, NewPostgresRequestStore(db), nil)
	return api, svc, requesterID, approverID, keyID
}

func postRequest(t *testing.T, api *RequestAPI, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/requests", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: userID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleSubmit(rec, req)
	return rec
}

func TestSubmitAPICreatesNewKeyRequest(t *testing.T) {
	api, _, requesterID, _, _ := requestAPIFixture(t)

	rec := postRequest(t, api, requesterID,
		`{"kind":"new_key","org_id":"gw","reason":"要一把","key_name":"我的密钥","models":["qwen-plus"]}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEmpty(t, got.ID)
	require.Equal(t, "pending", got.Status)
}

func TestSubmitAPIRequiresModelsForNewKey(t *testing.T) {
	// 与 P1.3a 的 POST /api/keys 同一条理由：上游把 models 缺失当成
	// 「放行全部模型」，这个 fail-open 只能在 API 边界堵。
	api, _, requesterID, _, _ := requestAPIFixture(t)

	rec := postRequest(t, api, requesterID,
		`{"kind":"new_key","org_id":"gw","reason":"x","key_name":"y"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "models_required")
}

func TestSubmitAPIRejectsUnknownKind(t *testing.T) {
	api, _, requesterID, _, _ := requestAPIFixture(t)

	rec := postRequest(t, api, requesterID,
		`{"kind":"something_else","org_id":"gw","reason":"x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_kind")
}

func TestSubmitAPIQuotaBumpRequiresExpiry(t *testing.T) {
	// 没有到期时间的「临时」提额就是永久提额，那是审批人没打算批的东西。
	api, _, requesterID, _, keyID := requestAPIFixture(t)

	rec := postRequest(t, api, requesterID,
		`{"kind":"quota_bump","org_id":"gw","reason":"x","target_key_id":"`+keyID+`","bump_to_budget":50}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "expiry_required")
}

func TestSubmitAPIRejectsMalformedBody(t *testing.T) {
	api, _, requesterID, _, _ := requestAPIFixture(t)
	rec := postRequest(t, api, requesterID, `{not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSubmitAPIUnknownOrgIs404(t *testing.T) {
	api, _, requesterID, _, _ := requestAPIFixture(t)

	rec := postRequest(t, api, requesterID,
		`{"kind":"new_key","org_id":"nope","reason":"x","key_name":"y","models":[]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListRequestsShowsOwnRequests(t *testing.T) {
	api, svc, requesterID, _, _ := requestAPIFixture(t)
	submitNewKey(t, svc, requesterID)

	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: requesterID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
}

func TestListRequestsEmptyIsArrayNotNull(t *testing.T) {
	api, _, requesterID, _, _ := requestAPIFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: requesterID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", rec.Body.String())
}

func actOn(
	t *testing.T, api *RequestAPI, userID, id string,
	h func(http.ResponseWriter, *http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/requests/"+id+"/act", nil)
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: userID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestApproveAPI(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)

	rec := actOn(t, api, approverID, r.ID, api.HandleApprove)
	require.Equal(t, http.StatusNoContent, rec.Code)

	got, err := svc.deps.Requests.Get(context.Background(), r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusApproved, got.Status)
}

func TestApproveAPIWithoutPermissionIs403(t *testing.T) {
	// 判定下沉到处理器意味着它必须自己拦住无授予的调用者——
	// 中间件在这条路由上只校验了「已登录」。
	api, svc, requesterID, _, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)

	rec := actOn(t, api, requesterID, r.ID, api.HandleApprove)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_denied")
}

func TestApproveAPIAlreadyDecidedIs409(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)
	require.Equal(t, http.StatusNoContent,
		actOn(t, api, approverID, r.ID, api.HandleApprove).Code)

	rec := actOn(t, api, approverID, r.ID, api.HandleApprove)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "request_not_pending")
}

func TestRejectAPI(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)

	rec := actOn(t, api, approverID, r.ID, api.HandleReject)
	require.Equal(t, http.StatusNoContent, rec.Code)

	got, err := svc.deps.Requests.Get(context.Background(), r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusRejected, got.Status)
}

func TestClaimAPIReturnsPlaintextOnce(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)
	require.Equal(t, http.StatusNoContent,
		actOn(t, api, approverID, r.ID, api.HandleApprove).Code)

	rec := actOn(t, api, requesterID, r.ID, api.HandleClaim)
	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.True(t, strings.HasPrefix(got.Key, "ak-"))
}

func TestClaimAPIByNonRequesterIs403(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)
	require.Equal(t, http.StatusNoContent,
		actOn(t, api, approverID, r.ID, api.HandleApprove).Code)

	rec := actOn(t, api, approverID, r.ID, api.HandleClaim)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "not_requester")
}

func TestClaimAPIBeforeApprovalIs409(t *testing.T) {
	api, svc, requesterID, _, _ := requestAPIFixture(t)
	r := submitNewKey(t, svc, requesterID)

	rec := actOn(t, api, requesterID, r.ID, api.HandleClaim)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "request_not_approved")
}

func TestToApproveAPIEmptyIsArrayNotNull(t *testing.T) {
	// 申请人没有 key:write，列表为空——但必须是 []，前端才不用处理 null。
	api, _, requesterID, _, _ := requestAPIFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/api/requests/to-approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: requesterID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleToApprove(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", rec.Body.String())
}

func TestToApproveAPIReturnsPendingForApprover(t *testing.T) {
	api, svc, requesterID, approverID, _ := requestAPIFixture(t)
	submitNewKey(t, svc, requesterID)

	req := httptest.NewRequest(http.MethodGet, "/api/requests/to-approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: approverID, Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleToApprove(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
}

func TestToApproveAPIUnknownUserIs404(t *testing.T) {
	// 上下文里的用户在库里不存在时，映射成 404 而不是 500。
	api, _, _, _, _ := requestAPIFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/api/requests/to-approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey,
		&User{ID: "nobody", Status: UserStatusActive}))
	rec := httptest.NewRecorder()
	api.HandleToApprove(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "user_not_found")
}
