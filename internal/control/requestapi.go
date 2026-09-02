package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type RequestAPI struct {
	svc      *ApprovalService
	requests RequestStore
	worker   *ApprovalWorker
}

// NewRequestAPI 组装申请相关的处理器。
//
// worker 可为 nil：审批后的 Nudge 是延迟优化，不是正确性要求，
// 周期扫描本就会兜住（与 P1.2c 同步器的 nudger 同款）。
func NewRequestAPI(
	svc *ApprovalService, requests RequestStore, worker *ApprovalWorker,
) *RequestAPI {
	return &RequestAPI{svc: svc, requests: requests, worker: worker}
}

// requestView 是申请单的对外表示。
type requestView struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	RequesterID   string     `json:"requester_id"`
	OrgID         string     `json:"org_id"`
	Reason        string     `json:"reason"`
	KeyName       *string    `json:"key_name"`
	Models        []string   `json:"models"`
	TargetKeyID   *string    `json:"target_key_id"`
	BumpToBudget  *float64   `json:"bump_to_budget"`
	BumpExpiresAt *time.Time `json:"bump_expires_at"`
	DecidedBy     *string    `json:"decided_by"`
	DecidedAt     *time.Time `json:"decided_at"`
	IssuedKeyID   *string    `json:"issued_key_id"`
	CreatedAt     time.Time  `json:"created_at"`
}

func requestViewOf(r *Request) requestView {
	return requestView{
		ID: r.ID, Kind: r.Kind, Status: r.Status, RequesterID: r.RequesterID,
		OrgID: r.OrgID, Reason: r.Reason, KeyName: r.KeyName,
		Models: orEmptyStrings(r.Models), TargetKeyID: r.TargetKeyID,
		BumpToBudget: r.BumpToBudget, BumpExpiresAt: r.BumpExpiresAt,
		DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt,
		IssuedKeyID: r.IssuedKeyID, CreatedAt: r.CreatedAt,
	}
}

// HandleSubmit 发起一张申请。
func (a *RequestAPI) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind          string     `json:"kind"`
		OrgID         string     `json:"org_id"`
		Reason        string     `json:"reason"`
		KeyName       string     `json:"key_name"`
		Models        *[]string  `json:"models"`
		TargetKeyID   string     `json:"target_key_id"`
		BumpToBudget  float64    `json:"bump_to_budget"`
		BumpExpiresAt *time.Time `json:"bump_expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}

	in := SubmitInput{
		Kind: body.Kind, RequesterID: u.ID, OrgID: body.OrgID, Reason: body.Reason,
		TargetKeyID: body.TargetKeyID, BumpToBudget: body.BumpToBudget,
		BumpExpiresAt: body.BumpExpiresAt,
	}

	switch body.Kind {
	case RequestKindNewKey:
		// 与 P1.3a 的 POST /api/keys 同一条理由：上游把 models 缺失
		// 当成放行全部模型，这个 fail-open 只能在边界堵。
		if body.Models == nil {
			writeError(w, http.StatusBadRequest, "models_required",
				"必须显式提供 models 字段；空数组表示放行全部模型")
			return
		}
		in.KeyName, in.Models = body.KeyName, *body.Models
	case RequestKindQuotaBump:
		// 没有到期时间的「临时」提额就是永久提额。
		if body.BumpExpiresAt == nil {
			writeError(w, http.StatusBadRequest, "expiry_required",
				"临时提额必须给出到期时间")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_kind",
			"kind 必须是 new_key 或 quota_bump")
		return
	}

	req, err := a.svc.Submit(r.Context(), in)
	if err != nil {
		writeRequestError(w, err, "提交申请失败")
		return
	}
	a.worker.Nudge()
	writeJSON(w, http.StatusCreated, requestViewOf(req))
}

// HandleList 列出与调用者相关的申请单。
//
// 本阶段只返回「我发起的」。审批人视角的待审列表要等 P1.4 的控制台
// 才有真实使用场景，届时按可见范围过滤（复用 GET /api/orgs 的先例）。
func (a *RequestAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	list, err := a.requests.ListByRequester(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询申请单失败")
		return
	}
	out := make([]requestView, 0, len(list))
	for _, req := range list {
		out = append(out, requestViewOf(req))
	}
	writeJSON(w, http.StatusOK, out)
}

// writeRequestError 把服务层的哨兵错误映射成 HTTP 状态码。
func writeRequestError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
	case errors.Is(err, ErrAPIKeyNotFound):
		writeError(w, http.StatusNotFound, "key_not_found", "目标密钥不存在")
	case errors.Is(err, ErrRequestNotFound):
		writeError(w, http.StatusNotFound, "request_not_found", "申请单不存在")
	case errors.Is(err, ErrRequestNotPending):
		writeError(w, http.StatusConflict, "request_not_pending", "该申请单已被处理过")
	case errors.Is(err, ErrRequestNotApproved):
		writeError(w, http.StatusConflict, "request_not_approved", "申请单尚未批准或已领取")
	case errors.Is(err, ErrNotRequester):
		writeError(w, http.StatusForbidden, "not_requester", "只有申请人本人可以领取")
	case errors.Is(err, ErrPermissionDenied):
		writeError(w, http.StatusForbidden, "permission_denied", "没有执行该操作的权限")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

// HandleApprove 批准一张申请单。
//
// 权限判定下沉到 ApprovalService：路径里只有 request ID，
// 中间件拿不到它归属的节点——与 DELETE /api/keys/{id} 同一类例外。
func (a *RequestAPI) HandleApprove(w http.ResponseWriter, r *http.Request) {
	a.decide(w, r, a.svc.Approve)
}

// HandleReject 驳回一张申请单。
func (a *RequestAPI) HandleReject(w http.ResponseWriter, r *http.Request) {
	a.decide(w, r, a.svc.Reject)
}

func (a *RequestAPI) decide(
	w http.ResponseWriter, r *http.Request,
	act func(ctx context.Context, id, deciderID string) error,
) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	if err := act(r.Context(), r.PathValue("id"), u.ID); err != nil {
		writeRequestError(w, err, "处理申请失败")
		return
	}
	a.worker.Nudge()
	w.WriteHeader(http.StatusNoContent)
}

// HandleClaim 让申请人领取已批准的新密钥。ak- 明文只在这里返回一次。
func (a *RequestAPI) HandleClaim(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	plaintext, k, err := a.svc.Claim(r.Context(), r.PathValue("id"), u.ID)
	if err != nil {
		writeRequestError(w, err, "领取密钥失败")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		keyView
		Key string `json:"key"`
	}{keyView: viewOf(k), Key: plaintext})
}
