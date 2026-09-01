package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/softmatrix/airlock/internal/authz"
)

type KeyAPI struct {
	issuer   *KeyIssuer
	keys     KeyStore
	resolver *authz.Resolver
}

// resolver 供 HandleRevoke 用：吊销的路径里只有密钥 ID，
// 中间件拿不到它所属的节点，判定只能下沉到处理器自己做（见 Task 11）。
func NewKeyAPI(issuer *KeyIssuer, keys KeyStore, resolver *authz.Resolver) *KeyAPI {
	return &KeyAPI{issuer: issuer, keys: keys, resolver: resolver}
}

// keyView 是密钥的对外表示。刻意不含 UpstreamKeyEnc——
// 上游密钥从不离开控制面，连字段名都不出现在响应里。
type keyView struct {
	ID             string     `json:"id"`
	KeyPrefix      string     `json:"key_prefix"`
	OrgID          string     `json:"org_id"`
	UserID         string     `json:"user_id"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	Models         []string   `json:"models"`
	MaxBudget      *float64   `json:"max_budget"`
	BudgetDuration *string    `json:"budget_duration"`
	RPMLimit       *int       `json:"rpm_limit"`
	TPMLimit       *int       `json:"tpm_limit"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func viewOf(k *APIKey) keyView {
	return keyView{
		ID: k.ID, KeyPrefix: k.KeyPrefix, OrgID: k.OrgID, UserID: k.UserID,
		Name: k.Name, Status: k.Status, Models: orEmptyStrings(k.Models),
		MaxBudget: k.MaxBudget, BudgetDuration: k.BudgetDuration,
		RPMLimit: k.RPMLimit, TPMLimit: k.TPMLimit,
		ExpiresAt: k.ExpiresAt, CreatedAt: k.CreatedAt,
	}
}

// HandleIssue 签发一把新密钥。ak- 明文只在这里返回一次。
func (a *KeyAPI) HandleIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID          string     `json:"org_id"`
		UserID         string     `json:"user_id"`
		Name           string     `json:"name"`
		Models         *[]string  `json:"models"`
		MaxBudget      *float64   `json:"max_budget"`
		BudgetDuration *string    `json:"budget_duration"`
		RPMLimit       *int       `json:"rpm_limit"`
		TPMLimit       *int       `json:"tpm_limit"`
		ExpiresAt      *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	// Models 用指针接收，才能区分「显式传了空数组」与「压根没传」。
	// 上游把两者都当成放行全部模型，这个 fail-open 只能在边界堵。
	if body.Models == nil {
		writeError(w, http.StatusBadRequest, "models_required",
			"必须显式提供 models 字段；空数组表示放行全部模型")
		return
	}

	plaintext, k, err := a.issuer.Issue(r.Context(), IssueRequest{
		OrgID: body.OrgID, UserID: body.UserID, Name: body.Name,
		Models: *body.Models, MaxBudget: body.MaxBudget,
		BudgetDuration: body.BudgetDuration,
		RPMLimit: body.RPMLimit, TPMLimit: body.TPMLimit,
		ExpiresAt: body.ExpiresAt,
	})
	if err != nil {
		writeKeyError(w, err, "签发密钥失败")
		return
	}

	writeJSON(w, http.StatusCreated, struct {
		keyView
		Key string `json:"key"`
	}{keyView: viewOf(k), Key: plaintext})
}

// HandleList 列出某节点下的密钥。
func (a *KeyAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	list, err := a.keys.ListByOrg(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询密钥失败")
		return
	}
	out := make([]keyView, 0, len(list))
	for _, k := range list {
		out = append(out, viewOf(k))
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleRevoke 吊销一把密钥。
//
// 权限判定下沉到这里：路径里只有密钥 ID，中间件拿不到它所属的节点，
// 与 DELETE /api/grants/{id} 是同一类例外。
func (a *KeyAPI) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	k, err := a.keys.Get(r.Context(), id)
	if err != nil {
		writeKeyError(w, err, "吊销密钥失败")
		return
	}

	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	allowed, err := a.resolver.Can(r.Context(), subjectOf(u), authz.PermKeyWrite, &k.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "没有吊销该密钥的权限")
		return
	}

	if err := a.issuer.Revoke(r.Context(), id); err != nil {
		writeKeyError(w, err, "吊销密钥失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeKeyError 把密钥相关的领域错误映射为合适的状态码。
func writeKeyError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
	case errors.Is(err, ErrOrgNotKeyHolder):
		writeError(w, http.StatusConflict, "org_not_key_holder",
			"该节点不是密钥边界，请先将其标记为 key holder")
	case errors.Is(err, ErrAPIKeyNotFound):
		writeError(w, http.StatusNotFound, "key_not_found", "密钥不存在")
	default:
		writeError(w, http.StatusBadGateway, "upstream_failed", fallback)
	}
}
