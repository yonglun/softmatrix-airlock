package authz

// 内置角色 ID。这些字符串会落进数据库并被授予引用，改动等于破坏性变更。
const (
	RoleAuditor         = "auditor"
	RoleDeveloper       = "developer"
	RoleFinOps          = "finops"
	RoleOrgAdmin        = "org_admin"
	RolePlatformAdmin   = "platform_admin"
	RoleSecurityOfficer = "security_officer"
)

// Role 是一个角色及其权限集。
type Role struct {
	ID          string
	Name        string
	Description string
	Permissions []string
}

// allPermissionKeys 返回全部权限键，供平台管理员使用。
// 用 All() 派生而不是手写一份清单：新增权限时平台管理员自动获得，
// 不会出现「加了权限却忘了给平台管理员」的漏配。
func allPermissionKeys() []string {
	perms := All()
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Key)
	}
	return out
}

// builtinRoles 按 ID 排序，保证预置迁移与接口返回的顺序稳定。
//
// 现阶段 security_officer 与 auditor 的权限集完全相同——区分二者的
// 护栏策略制定与误报申诉处理要到 P4 才有对应权限。这里如实标注，
// 不假装它们已经不同。
var builtinRoles = []Role{
	{
		ID:          RoleAuditor,
		Name:        "审计员",
		Description: "只读：查看审计日志与组织结构",
		Permissions: []string{PermAuditRead, PermOrgRead},
	},
	{
		ID:          RoleDeveloper,
		Name:        "开发者",
		Description: "普通成员基线：查看自己所属组织的结构",
		Permissions: []string{PermOrgRead},
	},
	{
		ID:          RoleFinOps,
		Name:        "财务 / FinOps",
		Description: "查看全公司成本与组织结构，用于成本归属与分摊",
		Permissions: []string{PermCostReadAll, PermOrgRead},
	},
	{
		ID:          RoleOrgAdmin,
		Name:        "组织管理员",
		Description: "管理被授予节点及其子树：组织结构、成员归属、角色授予",
		Permissions: []string{
			PermGrantRead, PermGrantWrite, PermMemberAssign,
			PermOrgDelete, PermOrgImport, PermOrgRead, PermOrgWrite,
		},
	},
	{
		ID:          RolePlatformAdmin,
		Name:        "平台管理员",
		Description: "全部权限",
		Permissions: allPermissionKeys(),
	},
	{
		ID:          RoleSecurityOfficer,
		Name:        "安全合规官",
		Description: "查看审计日志与组织结构（护栏策略权限在 P4 补齐）",
		Permissions: []string{PermAuditRead, PermOrgRead},
	},
}

// BuiltinRoles 返回全部内置角色的副本，按 ID 排序。
func BuiltinRoles() []Role {
	out := make([]Role, len(builtinRoles))
	for i, r := range builtinRoles {
		perms := make([]string, len(r.Permissions))
		copy(perms, r.Permissions)
		out[i] = Role{ID: r.ID, Name: r.Name, Description: r.Description, Permissions: perms}
	}
	return out
}
