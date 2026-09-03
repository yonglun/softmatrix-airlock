package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/softmatrix/airlock/internal/authz"
)

// AccessMode 声明一个端点的访问要求。
//
// 零值是 AccessUndeclared 而不是任何一种「有效」模式，这是刻意的：
// 新加端点忘了声明时它停在未声明状态，机械检查会直接拦下。
// 默认放行正是 P1.2a 那条「管理接口只校验已登录」缺陷的根因；
// 默认拒绝则会让人以为忘了声明也安全，从而不去把权限想清楚。
type AccessMode int

const (
	AccessUndeclared AccessMode = iota
	// AccessPublic 无需登录。
	AccessPublic
	// AccessAuthenticated 需登录，但不校验权限（如查看自己的身份）。
	AccessAuthenticated
	// AccessPermission 需登录且校验 Permission。
	AccessPermission
)

// TargetExtractor 从请求里取出判定用的目标节点 ID。返回 nil 表示无特定目标。
type TargetExtractor func(r *http.Request) (*string, error)

// Route 是一条路由声明。
type Route struct {
	Pattern    string
	Access     AccessMode
	Permission string          // Access 为 AccessPermission 时必填
	Target     TargetExtractor // Access 为 AccessPermission 时必填
	Handler    http.HandlerFunc
}

// TargetFromPath 从路径参数取目标节点。
func TargetFromPath(name string) TargetExtractor {
	return func(r *http.Request) (*string, error) {
		v := r.PathValue(name)
		if v == "" {
			return nil, nil
		}
		return &v, nil
	}
}

// TargetFromBody 从 JSON 请求体的某个字段取目标节点。
//
// 读完必须把请求体放回去——否则后面的处理器拿到的是一个已被读空的 body。
// 请求体不是合法 JSON 时按「无目标」处理而不是报错：那是处理器该返回的
// 400 invalid_body，不该在判定阶段变成 500。
func TargetFromBody(field string) TargetExtractor {
	return func(r *http.Request) (*string, error) {
		if r.Body == nil {
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))

		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil
		}
		v, ok := m[field].(string)
		if !ok || v == "" {
			return nil, nil
		}
		return &v, nil
	}
}

// TargetGlobal 表示这次调用没有特定目标节点，按边界规则要求全局授予。
func TargetGlobal() TargetExtractor {
	return func(*http.Request) (*string, error) { return nil, nil }
}

// subjectOf 把 control.User 转成 authz 判定用的主体。
func subjectOf(u *User) authz.Subject {
	return authz.Subject{
		UserID:       u.ID,
		Active:       u.Status == UserStatusActive,
		PrimaryOrgID: u.PrimaryOrgID,
	}
}

// enforce 给一条路由套上判定中间件。
func (s *Server) enforce(rt Route) http.HandlerFunc {
	if rt.Access != AccessPermission {
		return rt.Handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
			return
		}

		target, err := rt.Target(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "无法解析请求体")
			return
		}

		allowed, err := s.deps.Resolver.Can(r.Context(), subjectOf(u), rt.Permission, target)
		if err != nil {
			if errors.Is(err, authz.ErrOrgNotFound) {
				writeError(w, http.StatusNotFound, "org_not_found", "组织节点不存在")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "permission_denied", "没有执行该操作的权限")
			return
		}
		rt.Handler(w, r)
	}
}

// DefaultRoutes 是管理面的全部路由声明。
//
// 加新端点必须在这里声明访问要求，否则 routes_test.go 的机械检查会失败。
// 这是刻意的：P1.2a 曾出现过「所有管理接口只校验已登录、不校验权限」的缺陷，
// 根因就是没有任何机制强迫开发者为新端点想清楚权限。
func DefaultRoutes(deps ServerDeps) []Route {
	// 依赖未装配时用占位处理器，让机械检查能在不构造完整依赖的情况下检查路由表。
	stub := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not_wired", "该端点未装配")
	}
	authH := func(pick func(*Auth) http.HandlerFunc) http.HandlerFunc {
		if deps.Auth == nil {
			return stub
		}
		return pick(deps.Auth)
	}
	orgH := func(pick func(*OrgAPI) http.HandlerFunc) http.HandlerFunc {
		if deps.OrgAPI == nil {
			return stub
		}
		return pick(deps.OrgAPI)
	}
	grantH := func(pick func(*GrantAPI) http.HandlerFunc) http.HandlerFunc {
		if deps.GrantAPI == nil {
			return stub
		}
		return pick(deps.GrantAPI)
	}
	syncH := func(pick func(*SyncAPI) http.HandlerFunc) http.HandlerFunc {
		if deps.SyncAPI == nil {
			return stub
		}
		return pick(deps.SyncAPI)
	}
	keyH := func(pick func(*KeyAPI) http.HandlerFunc) http.HandlerFunc {
		if deps.KeyAPI == nil {
			return stub
		}
		return pick(deps.KeyAPI)
	}
	consoleH := func() http.HandlerFunc {
		if deps.ConsoleFS == nil {
			// "/" 是通配兜底，捕获了此前完全没有路由匹配、原本会得到
			// 普通 404 的一切路径。这里不能用 stub（501「未装配」）：
			// 那是给「这条具体端点存在但没接线」用的，而未配置控制台时
			// 语义是「这里本来就没有页面」，该是 404，不是 501。
			return http.NotFound
		}
		return ConsoleHandler(deps.ConsoleFS)
	}
	reqH := func(pick func(*RequestAPI) http.HandlerFunc) http.HandlerFunc {
		if deps.RequestAPI == nil {
			return stub
		}
		return pick(deps.RequestAPI)
	}

	return []Route{
		// ---- 公开：无需登录 ----
		{
			Pattern: "GET /healthz", Access: AccessPublic,
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			},
		},
		{Pattern: "GET /auth/login", Access: AccessPublic,
			Handler: authH(func(a *Auth) http.HandlerFunc { return a.HandleLogin })},
		{Pattern: "GET /auth/callback", Access: AccessPublic,
			Handler: authH(func(a *Auth) http.HandlerFunc { return a.HandleCallback })},
		{Pattern: "POST /auth/logout", Access: AccessPublic,
			Handler: authH(func(a *Auth) http.HandlerFunc { return a.HandleLogout })},

		// ---- 已登录即可：查看自己的身份 ----
		{
			Pattern: "GET /api/whoami", Access: AccessAuthenticated,
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleWhoami }),
		},

		// ---- 组织树 ----
		{
			// 按可见范围过滤发生在 HandleList 内部（无权限时返回 200+[]，
			// 而不是 403——这是列表类接口的正确行为，与报错拒绝不同）。
			// 中间件因此不做单一目标的权限判定，只要求已登录，
			// 与 DELETE /api/grants/{id} 把判定下沉到处理器是同一类例外。
			Pattern: "GET /api/orgs", Access: AccessAuthenticated,
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleList }),
		},
		{
			// parent_id 为空表示建根节点，此时目标为 nil，按边界规则要求全局授予
			Pattern: "POST /api/orgs", Access: AccessPermission,
			Permission: authz.PermOrgWrite, Target: TargetFromBody("parent_id"),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleCreate }),
		},
		{
			Pattern: "PATCH /api/orgs/{id}/name", Access: AccessPermission,
			Permission: authz.PermOrgWrite, Target: TargetFromPath("id"),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleRename }),
		},
		{
			// 中间件只校验源节点；目标父节点的权限由处理器再查一次（见 Task 14）
			Pattern: "PATCH /api/orgs/{id}/parent", Access: AccessPermission,
			Permission: authz.PermOrgWrite, Target: TargetFromPath("id"),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleMove }),
		},
		{
			Pattern: "DELETE /api/orgs/{id}", Access: AccessPermission,
			Permission: authz.PermOrgDelete, Target: TargetFromPath("id"),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleDelete }),
		},
		{
			Pattern: "PUT /api/orgs/{id}/key-holder", Access: AccessPermission,
			Permission: authz.PermOrgWrite, Target: TargetFromPath("id"),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleSetKeyHolder }),
		},

		// ---- 通讯录导入：作用面覆盖整棵树，要求全局授予 ----
		{
			Pattern: "GET /api/orgs/import/preview", Access: AccessPermission,
			Permission: authz.PermOrgImport, Target: TargetGlobal(),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleImportPreview }),
		},
		{
			Pattern: "POST /api/orgs/import/apply", Access: AccessPermission,
			Permission: authz.PermOrgImport, Target: TargetGlobal(),
			Handler: orgH(func(o *OrgAPI) http.HandlerFunc { return o.HandleImportApply }),
		},

		// ---- 角色授予与成员归属 ----
		{
			Pattern: "GET /api/roles", Access: AccessAuthenticated,
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleListRoles }),
		},
		{
			Pattern: "GET /api/orgs/{id}/grants", Access: AccessPermission,
			Permission: authz.PermGrantRead, Target: TargetFromPath("id"),
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleListGrants }),
		},
		{
			Pattern: "POST /api/grants", Access: AccessPermission,
			Permission: authz.PermGrantWrite, Target: TargetFromBody("org_id"),
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleCreateGrant }),
		},
		{
			// 撤销授予要判定的是「授予所在的节点」，而路径里只有授予 ID，
			// 中间件拿不到目标节点。判定下沉到 HandleDeleteGrant 自己做。
			Pattern: "DELETE /api/grants/{id}", Access: AccessAuthenticated,
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleDeleteGrant }),
		},
		{
			Pattern: "PUT /api/users/{id}/primary-org", Access: AccessPermission,
			Permission: authz.PermMemberAssign, Target: TargetFromBody("org_id"),
			Handler: grantH(func(g *GrantAPI) http.HandlerFunc { return g.HandleAssignPrimaryOrg }),
		},

		// ---- LiteLLM 同步：平台级集成状态，只有平台管理员看得到 ----
		{
			Pattern: "GET /api/litellm/sync/status", Access: AccessPermission,
			Permission: authz.PermPlatformConfigure, Target: TargetGlobal(),
			Handler: syncH(func(s *SyncAPI) http.HandlerFunc { return s.HandleStatus }),
		},
		{
			Pattern: "POST /api/litellm/sync", Access: AccessPermission,
			Permission: authz.PermPlatformConfigure, Target: TargetGlobal(),
			Handler: syncH(func(s *SyncAPI) http.HandlerFunc { return s.HandleTrigger }),
		},

		// ---- 虚拟密钥 ----
		{
			Pattern: "POST /api/keys", Access: AccessPermission,
			Permission: authz.PermKeyWrite, Target: TargetFromBody("org_id"),
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleIssue }),
		},
		{
			Pattern: "GET /api/orgs/{id}/keys", Access: AccessPermission,
			Permission: authz.PermKeyRead, Target: TargetFromPath("id"),
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleList }),
		},
		{
			// 路径里只有密钥 ID，中间件拿不到它所属的节点。
			// 判定下沉到 HandleRevoke 自己做，与 DELETE /api/grants/{id} 同理。
			Pattern: "DELETE /api/keys/{id}", Access: AccessAuthenticated,
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleRevoke }),
		},
		{
			// 只返回「我是责任人」的密钥，按调用者本人过滤，
			// 因此不需要节点级判定——与 GET /api/requests 同一先例。
			Pattern: "GET /api/keys/mine", Access: AccessAuthenticated,
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleMine }),
		},

		// ---- 申请与审批 ----
		{
			Pattern: "POST /api/requests", Access: AccessPermission,
			Permission: authz.PermKeyRequest, Target: TargetFromBody("org_id"),
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleSubmit }),
		},
		{
			// 只返回「我发起的」，按调用者本人过滤，因此不需要节点级判定。
			Pattern: "GET /api/requests", Access: AccessAuthenticated,
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleList }),
		},
		{
			// 审批人视角的待审列表。可见范围在处理器内按 key:write 的
			// Scopes 过滤，与 GET /api/orgs 同一先例。
			Pattern: "GET /api/requests/to-approve", Access: AccessAuthenticated,
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleToApprove }),
		},
		{
			// 以下三条路径里只有 request ID，中间件拿不到它归属的节点，
			// 判定下沉到处理器自己做，与 DELETE /api/keys/{id} 同理。
			Pattern: "POST /api/requests/{id}/approve", Access: AccessAuthenticated,
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleApprove }),
		},
		{
			Pattern: "POST /api/requests/{id}/reject", Access: AccessAuthenticated,
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleReject }),
		},
		{
			Pattern: "POST /api/requests/{id}/claim", Access: AccessAuthenticated,
			Handler: reqH(func(a *RequestAPI) http.HandlerFunc { return a.HandleClaim }),
		},

		// ---- 轮换与批量吊销 ----
		{
			// 路径里只有密钥 ID，中间件拿不到它所属的节点，
			// 判定下沉到 HandleRotate 自己做（责任人本人或节点上的 key:write）。
			Pattern: "POST /api/keys/{id}/rotate", Access: AccessAuthenticated,
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleRotate }),
		},
		{
			Pattern: "POST /api/orgs/{id}/keys/revoke", Access: AccessPermission,
			Permission: authz.PermKeyWrite, Target: TargetFromPath("id"),
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleRevokeOrg }),
		},
		{
			// 全系统最具破坏性的一次调用，单开一个全局权限：
			// 塞进 platform:configure 里等于谁能改配置谁就能清空全公司凭据。
			Pattern: "POST /api/keys/revoke-all", Access: AccessPermission,
			Permission: authz.PermKeyRevokeAll, Target: TargetGlobal(),
			Handler: keyH(func(k *KeyAPI) http.HandlerFunc { return k.HandleRevokeAll }),
		},

		// ---- 控制台静态站 ----
		{
			// 内层 mux 的兜底：拼错的 /api/xxx 与用错方法的请求都落到这里，
			// 返回统一形状的 JSON 404 而不是 Go 默认的 text/plain。
			Pattern: "/api/", Access: AccessAuthenticated,
			Handler: APINotFoundHandler(),
		},
		{
			// 必须是方法无关的 "/"：写成 "GET /" 会与 "/api/" 冲突，
			// ServeMux 在注册时就 panic。
			Pattern: "/", Access: AccessPublic,
			Handler: consoleH(),
		},
	}
}
