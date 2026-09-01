package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/softmatrix/airlock/internal/authz"
)

// OrgAPI 提供组织树与通讯录导入的 HTTP 接口。
type OrgAPI struct {
	store    OrgStore
	source   DirectorySource
	resolver *authz.Resolver
	nudger   *Syncer
}

func NewOrgAPI(store OrgStore, source DirectorySource, resolver *authz.Resolver) *OrgAPI {
	return &OrgAPI{store: store, source: source, resolver: resolver}
}

// WithNudger 装上同步器。
//
// 不做成构造器参数是因为它是可选依赖（未配 LITELLM_MASTER_KEY 时同步整体禁用），
// 而 NewOrgAPI 有 9 个调用点、其中 8 个在测试里——为一个可选依赖 churn 掉
// 8 个测试文件不值得。nudger 为 nil 时 Nudge 是 no-op。
func (a *OrgAPI) WithNudger(s *Syncer) *OrgAPI {
	a.nudger = s
	return a
}

// HandleList 返回调用者可见范围内的组织节点。
//
// 可见范围以 org:read 这一条权限为准，而不是「持有任何授予即可见」——
// P1.4 出现自定义角色后会有不含 org:read 的角色，那种授予不该带来可见性。
func (a *OrgAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}

	all, err := a.store.All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询组织树失败")
		return
	}

	visible, err := a.visibleOrgs(r.Context(), u, all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "计算可见范围失败")
		return
	}
	writeJSON(w, http.StatusOK, visible)
}

// visibleOrgs 按可见范围过滤组织节点。
//
// 可见集合 = 持有 org:read 的节点各自的整棵子树 + 归属节点的子树 + 上述节点的祖先链。
// 祖先链必须给——否则前端拿到的是一片没有根的孤立子树，渲染不出层级。
// 它只暴露节点名与父子关系，这个泄漏面可以接受。
func (a *OrgAPI) visibleOrgs(ctx context.Context, u *User, all []*Org) ([]*Org, error) {
	global, nodes, err := a.resolver.Scopes(ctx, subjectOf(u), authz.PermOrgRead)
	if err != nil {
		return nil, err
	}
	if global {
		return all, nil
	}

	// 隐式开发者基线：归属节点的子树同样可见。
	if u.PrimaryOrgID != nil {
		nodes = append(nodes, *u.PrimaryOrgID)
	}
	if len(nodes) == 0 {
		return []*Org{}, nil
	}

	byID := make(map[string]*Org, len(all))
	for _, o := range all {
		byID[o.ID] = o
	}

	// 收集作用域根节点的路径，用于前缀判断
	var roots []string
	for _, id := range nodes {
		if o, ok := byID[id]; ok {
			roots = append(roots, o.Path)
		}
	}

	keep := map[string]bool{}
	for _, o := range all {
		for _, root := range roots {
			// 子树：加分隔符再比前缀，避免 /root/rd 吞掉同前缀兄弟 /root/rd2
			inSubtree := o.Path == root || strings.HasPrefix(o.Path, root+"/")
			// 祖先：作用域根节点的路径以本节点路径开头
			isAncestor := strings.HasPrefix(root, o.Path+"/")
			if inSubtree || isAncestor {
				keep[o.ID] = true
				break
			}
		}
	}

	out := make([]*Org, 0, len(keep))
	for _, o := range all {
		if keep[o.ID] {
			out = append(out, o)
		}
	}
	return out, nil
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
	a.nudger.Nudge()
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
	a.nudger.Nudge()
	w.WriteHeader(http.StatusNoContent)
}

// HandleMove 把节点移到新的父节点之下。
//
// 中间件已经校验了源节点（路径里的 {id}）上的 org:write。
// 这里必须再校验目标父节点——只查一端都能构造越权：
// 只查源端，能把自己管的子树塞进别人的部门；
// 只查目标端，能把别人的子树拽到自己名下。
func (a *OrgAPI) HandleMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ParentID *string `json:"parent_id"`
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
	allowed, err := a.resolver.Can(r.Context(), subjectOf(u), authz.PermOrgWrite, body.ParentID)
	if err != nil {
		if errors.Is(err, authz.ErrOrgNotFound) {
			writeError(w, http.StatusNotFound, "org_not_found", "目标父节点不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "没有在目标父节点下添加子节点的权限")
		return
	}

	if err := a.store.Move(r.Context(), r.PathValue("id"), body.ParentID); err != nil {
		writeOrgError(w, err, "移动组织节点失败")
		return
	}
	a.nudger.Nudge()
	w.WriteHeader(http.StatusNoContent)
}

func (a *OrgAPI) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 先读一份：删掉之后就查不到它的 path 与标记了，
	// 而删除传播需要这两样来判断上游该删哪类实体。
	o, err := a.store.Get(r.Context(), id)
	if err != nil {
		writeOrgError(w, err, "删除组织节点失败")
		return
	}
	if err := a.store.Delete(r.Context(), id); err != nil {
		writeOrgError(w, err, "删除组织节点失败")
		return
	}
	a.nudger.DropNode(r.Context(), o)
	w.WriteHeader(http.StatusNoContent)
}

// HandleSetKeyHolder 标记或取消该节点的密钥边界身份。
//
// 标记后该节点会在下一轮同步里成为一个 LiteLLM Team；
// 取消标记不会删掉已有的 Team——见设计文档 §6「一个刻意留下的不对称」。
func (a *OrgAPI) HandleSetKeyHolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IsKeyHolder bool `json:"is_key_holder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}

	id := r.PathValue("id")
	// 只在「取消」时把关。不回收上游 Team（见设计文档 D7），所以取消本身
	// 无副作用，但会留下「不再是密钥边界却挂着在用密钥」的矛盾状态。
	if !body.IsKeyHolder {
		n, err := a.store.CountLiveKeys(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "统计在用密钥失败")
			return
		}
		if n > 0 {
			writeError(w, http.StatusConflict, "org_has_live_keys",
				"该节点下还有未吊销的密钥，请先吊销后再取消密钥边界标记")
			return
		}
	}
	if err := a.store.SetKeyHolder(r.Context(), id, body.IsKeyHolder); err != nil {
		writeOrgError(w, err, "设置密钥边界标记失败")
		return
	}
	a.nudger.Nudge()
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
// HandleImportApply 应用用户勾选的差异项。
//
// 客户端只提交「勾选了哪些 external_id」，服务端重新拉取通讯录、重新计算差异。
// 这样客户端完全失去对 Kind / Name / OrgID / ParentExternalID 的控制权——
// 否则可以伪造一条 LDAP 里不存在的 added 项，在本地建出一个 external_id
// 由攻击者指定的节点，让真实的目录节点之后永远被判定为「已存在」而不再导入。
//
// 代价是多一次通讯录拉取。预览与应用之间目录发生变化时，服务端按最新事实动作
// （更安全），并把实际执行的差异项回传给调用方。
func (a *OrgAPI) HandleImportApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExternalIDs []string `json:"external_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}

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

	selected := make(map[string]bool, len(body.ExternalIDs))
	for _, id := range body.ExternalIDs {
		selected[id] = true
	}

	var items []DiffItem
	for _, it := range ComputeDiff(remote, local, a.source.Name()) {
		if selected[it.ExternalID] {
			items = append(items, it)
		}
	}

	res, err := ApplyImport(r.Context(), a.store, a.source.Name(), items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "import_failed", "应用导入变更失败")
		return
	}
	if items == nil {
		items = []DiffItem{}
	}
	a.nudger.Nudge()
	writeJSON(w, http.StatusOK, map[string]any{
		"Created":      res.Created,
		"Renamed":      res.Renamed,
		"MarkedOrphan": res.MarkedOrphan,
		"Skipped":      res.Skipped,
		"applied":      items,
	})
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
	case errors.Is(err, ErrOrgHasUsers):
		writeError(w, http.StatusConflict, "org_has_users", "该节点下还有归属用户，无法删除")
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
