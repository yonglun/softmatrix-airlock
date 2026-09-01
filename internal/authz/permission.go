// Package authz 承载 Airlock 的权限模型与判定逻辑。
//
// 本包是纯逻辑：不含 HTTP、不含 SQL、不含 OIDC，判定所需的数据
// 全部通过 Store 接口取。因此权限判定的每一个分支都能脱离数据库测到底，
// 而这正是最容易藏 bug 的地方。
//
// 本包不 import internal/control 与 internal/edge。
package authz

import "sort"

// Scope 是一条权限的作用域类型，决定它能被什么样的授予赋予。
type Scope string

const (
	// ScopeGlobal 的权限只能由全局授予（org_id 为 NULL）赋予。
	// 这挡住「把平台管理员授予在某个节点上」时泄漏出 SSO 配置这类全局能力。
	ScopeGlobal Scope = "global"

	// ScopeOrg 的权限由落在目标节点祖先链上的授予赋予；全局授予同样算数。
	ScopeOrg Scope = "org"
)

// 权限键。定义在代码里而不是数据库里——作用域是权限的固有属性，
// 不是运维可编辑的数据。数据库的 role_permissions 只存这些字符串。
const (
	PermOrgRead      = "org:read"
	PermOrgWrite     = "org:write"
	PermOrgDelete    = "org:delete"
	PermOrgImport    = "org:import"
	PermMemberAssign = "member:assign"
	PermGrantRead    = "grant:read"
	PermGrantWrite   = "grant:write"
	PermKeyRead      = "key:read"
	PermKeyWrite     = "key:write"
	PermKeyRequest   = "key:request"

	PermAuditRead         = "audit:read"
	PermCostReadAll       = "cost:read_all"
	PermPlatformConfigure = "platform:configure"
)

// Permission 是一条权限的完整定义。
type Permission struct {
	Key   string
	Scope Scope
	Desc  string
}

// registry 是权限的唯一真相来源。
//
// audit:read / cost:read_all / platform:configure 在 P1.2b 阶段还没有对应端点，
// 但必须现在定义——否则 6 个内置角色的权限集彼此无法区分，
// 「平台管理员」与「组织管理员」会长得一模一样。
var registry = map[string]Permission{
	PermOrgRead:      {Key: PermOrgRead, Scope: ScopeOrg, Desc: "查看组织树"},
	PermOrgWrite:     {Key: PermOrgWrite, Scope: ScopeOrg, Desc: "创建、改名、移动组织节点"},
	PermOrgDelete:    {Key: PermOrgDelete, Scope: ScopeOrg, Desc: "删除组织节点"},
	PermOrgImport:    {Key: PermOrgImport, Scope: ScopeOrg, Desc: "通讯录导入预览与应用"},
	PermMemberAssign: {Key: PermMemberAssign, Scope: ScopeOrg, Desc: "指派用户的组织归属"},
	PermGrantRead:    {Key: PermGrantRead, Scope: ScopeOrg, Desc: "查看角色授予"},
	PermGrantWrite:   {Key: PermGrantWrite, Scope: ScopeOrg, Desc: "授予与撤销角色"},
	PermKeyRead:      {Key: PermKeyRead, Scope: ScopeOrg, Desc: "查看节点下签发的密钥"},
	PermKeyWrite:     {Key: PermKeyWrite, Scope: ScopeOrg, Desc: "签发与吊销密钥"},
	PermKeyRequest:   {Key: PermKeyRequest, Scope: ScopeOrg, Desc: "发起密钥与提额申请"},

	PermAuditRead:         {Key: PermAuditRead, Scope: ScopeGlobal, Desc: "查看审计日志"},
	PermCostReadAll:       {Key: PermCostReadAll, Scope: ScopeGlobal, Desc: "查看全公司成本"},
	PermPlatformConfigure: {Key: PermPlatformConfigure, Scope: ScopeGlobal, Desc: "配置 SSO、模型供应商与 License"},
}

// Lookup 按键查一条权限。
func Lookup(key string) (Permission, bool) {
	p, ok := registry[key]
	return p, ok
}

// IsKnown 判断权限键是否已注册。
// 启动时用它校验数据库里的权限字符串，挡住迁移写错或版本回退留下的脏数据。
func IsKnown(key string) bool {
	_, ok := registry[key]
	return ok
}

// All 返回全部权限，按键排序。
// 排序是为了让预置迁移与控制台展示的顺序稳定——遍历 map 的顺序是随机的。
func All() []Permission {
	out := make([]Permission, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
