package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// OrgAPI 提供组织树与通讯录导入的 HTTP 接口。
type OrgAPI struct {
	store  OrgStore
	source DirectorySource
}

func NewOrgAPI(store OrgStore, source DirectorySource) *OrgAPI {
	return &OrgAPI{store: store, source: source}
}

func (a *OrgAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	orgs, err := a.store.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询组织树失败")
		return
	}
	writeJSON(w, http.StatusOK, orgs)
}

func (a *OrgAPI) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "组织名称不能为空")
		return
	}

	o := &Org{ID: uuid.NewString(), Name: name, ParentID: body.ParentID}
	if err := a.store.Create(r.Context(), o); err != nil {
		writeOrgError(w, err, "创建组织节点失败")
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

func (a *OrgAPI) HandleRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "组织名称不能为空")
		return
	}
	if err := a.store.Rename(r.Context(), r.PathValue("id"), name); err != nil {
		writeOrgError(w, err, "重命名失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *OrgAPI) HandleMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentID *string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	if err := a.store.Move(r.Context(), r.PathValue("id"), body.ParentID); err != nil {
		writeOrgError(w, err, "移动组织节点失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *OrgAPI) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeOrgError(w, err, "删除组织节点失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleImportPreview 拉取 IdP 组织树并计算差异，绝不改动任何数据。
func (a *OrgAPI) HandleImportPreview(w http.ResponseWriter, r *http.Request) {
	remote, err := a.source.FetchOrgTree(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "directory_unreachable", "读取通讯录失败")
		return
	}
	local, err := a.store.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询组织树失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": a.source.Name(),
		"items":  ComputeDiff(remote, local, a.source.Name()),
	})
}

// HandleImportApply 应用用户确认过的差异项。
func (a *OrgAPI) HandleImportApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []DiffItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	res, err := ApplyImport(r.Context(), a.store, a.source.Name(), body.Items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "import_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeOrgError 把领域错误映射为合适的 HTTP 状态，
// 而不是把数据库错误原样抛给调用方。
func writeOrgError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
	case errors.Is(err, ErrOrgHasChildren):
		writeError(w, http.StatusConflict, "org_has_children", "该节点下还有子节点，无法删除")
	case errors.Is(err, ErrOrgHasKeys):
		writeError(w, http.StatusConflict, "org_has_keys", "该节点下还有密钥，无法删除")
	case errors.Is(err, ErrOrgCycle):
		writeError(w, http.StatusConflict, "org_cycle", "不能把节点移动到自己的子树下")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
