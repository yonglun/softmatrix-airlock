package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/google/uuid"

	"github.com/softmatrix/airlock/internal/authz"
)

// GrantAPI 提供角色、授予与成员归属的 HTTP 接口。
type GrantAPI struct {
	users    UserStore
	rbac     RBACStore
	resolver *authz.Resolver
}

func NewGrantAPI(users UserStore, rbac RBACStore, resolver *authz.Resolver) *GrantAPI {
	return &GrantAPI{users: users, rbac: rbac, resolver: resolver}
}

// HandleListRoles 列出全部角色。任何已登录用户都能看——
// 知道系统里有哪些角色不构成信息泄漏，而授予界面需要它。
func (a *GrantAPI) HandleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := a.rbac.ListRoles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询角色失败")
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

// HandleListGrants 列出某个组织节点上的授予。
func (a *GrantAPI) HandleListGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := a.rbac.ListGrantsForOrg(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询授予失败")
		return
	}
	writeJSON(w, http.StatusOK, grants)
}

// HandleCreateGrant 授予角色。
//
// 中间件已校验授予者在目标节点上有 grant:write。这里还要再过一道防提权：
// 被授予角色的权限集必须是授予者在该节点上已持有权限的子集，
// 否则组织管理员可以给自己授予一个含全局权限的角色间接提权。
func (a *GrantAPI) HandleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string  `json:"user_id"`
		RoleID string  `json:"role_id"`
		OrgID  *string `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}
	if body.UserID == "" || body.RoleID == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", "user_id 与 role_id 不能为空")
		return
	}

	granter, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}

	roles, err := a.rbac.ListRoles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询角色失败")
		return
	}
	known := false
	for _, role := range roles {
		if role.ID == body.RoleID {
			known = true
			break
		}
	}
	if !known {
		writeError(w, http.StatusBadRequest, "role_not_found", "角色不存在")
		return
	}

	allowed, err := a.resolver.CanGrant(r.Context(), subjectOf(granter), body.RoleID, body.OrgID)
	if err != nil {
		if errors.Is(err, authz.ErrOrgNotFound) {
			writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "escalation_denied",
			"不能授予自己不持有的权限")
		return
	}

	if _, err := a.users.ByID(r.Context(), body.UserID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user_not_found", "用户不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "查询用户失败")
		return
	}

	g := RoleGrant{
		ID: uuid.NewString(), UserID: body.UserID, RoleID: body.RoleID,
		OrgID: body.OrgID, GrantedBy: &granter.ID,
	}
	if err := a.rbac.CreateGrant(r.Context(), g); err != nil {
		writeError(w, http.StatusConflict, "grant_conflict", "该授予已存在或无法创建")
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// HandleDeleteGrant 撤销授予。
//
// 这条路由在路由表里声明为 AccessAuthenticated 而非 AccessPermission，
// 因为要判定的节点是「授予所在的节点」，而路径里只有授予 ID——
// 中间件拿不到目标节点，判定必须下沉到这里。这是有意的例外。
func (a *GrantAPI) HandleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	g, err := a.rbac.GetGrant(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrGrantNotFound) {
			writeError(w, http.StatusNotFound, "grant_not_found", "授予不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "查询授予失败")
		return
	}

	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}
	allowed, err := a.resolver.Can(r.Context(), subjectOf(u), authz.PermGrantWrite, g.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "没有撤销该授予的权限")
		return
	}

	if err := a.rbac.DeleteGrant(r.Context(), g.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "撤销授予失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleWhoami 返回当前用户的画像、角色授予与全局权限集。
//
// P1.2a 时这里直接吐整个 User 结构（含 IsPlatformAdmin 布尔位）。
// 那个布尔位已经删掉，改为返回授予与解析出的全局权限——
// 对将来的控制台也更有用：它需要据此决定显示哪些工作台。
func (a *GrantAPI) HandleWhoami(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}

	grants, err := a.rbac.ListGrantsForUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询授予失败")
		return
	}
	if grants == nil {
		grants = []RoleGrant{}
	}

	perms, err := a.resolver.Permissions(r.Context(), subjectOf(u), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "解析权限失败")
		return
	}
	global := make([]string, 0, len(perms))
	for p := range perms {
		global = append(global, p)
	}
	sort.Strings(global) // 顺序稳定，前端渲染才不会每次都跳

	// 工作台可见性由服务端算：前端拿不到角色的权限集（/api/roles 不返回它），
	// 而 global_permissions 又会漏掉节点级授予——两条合起来意味着这个判定
	// 只能放在这里（设计文档 D2）。
	workbenches, err := Workbenches(r.Context(), a.resolver, subjectOf(u))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "计算工作台失败")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":               u,
		"grants":             grants,
		"global_permissions": global,
		"workbenches":        workbenches,
	})
}

// HandleAssignPrimaryOrg 指派或清除用户的组织归属。
//
// 归属决定两件事：该用户名下 Key 的默认计费归属（P1.3），
// 以及隐式开发者基线的作用范围（他在自己归属子树内自动可读）。
func (a *GrantAPI) HandleAssignPrimaryOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID *string `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "请求体不是合法 JSON")
		return
	}

	if err := a.users.AssignPrimaryOrg(r.Context(), r.PathValue("id"), body.OrgID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			writeError(w, http.StatusNotFound, "user_not_found", "用户不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "指派组织归属失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
