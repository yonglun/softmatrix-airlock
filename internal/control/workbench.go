package control

import (
	"context"

	"github.com/softmatrix/airlock/internal/authz"
)

// 工作台标识。与前端 web/src/components/workbenches.ts 里的 id 一一对应。
const (
	WorkbenchMySpace  = "my-space"
	WorkbenchPlatform = "platform"
	WorkbenchFinOps   = "finops"
	WorkbenchSecurity = "security"
)

// workbenchRule 是一个工作台的出现条件。
//
// 判定口径（设计文档 D3）：一个工作台只在「其中至少有一个本期已实现、
// 且该用户有权使用的页面」时才出现——可见性严格等于「有页面可用」。
// 因此不会出现点进去只有 403 的空壳工作台。
//
// 这张表随 P1.4b/c 加页面要跟着长，但它只在这一处。
var workbenchRules = []struct {
	id string
	// requires 为空表示恒可见；否则「在任意位置持有其一」即可见。
	requires []string
}{
	{id: WorkbenchMySpace}, // 我的申请：登录即可用
	{id: WorkbenchPlatform, requires: []string{authz.PermOrgWrite}}, // 组织与成员、虚拟密钥、角色与权限
	{
		// 成本财务下有两类页面：提额审批（要 key:write）与成本总览
		// （要 cost:read 或 cost:read_all）。持其一即可见——否则
		// finops 角色（只有 cost:read_all）会看不到为他造的工作台。
		id:       WorkbenchFinOps,
		requires: []string{authz.PermKeyWrite, authz.PermCostRead, authz.PermCostReadAll},
	},
	{id: WorkbenchSecurity, requires: []string{authz.PermAuditRead}}, // 审计检索
}

// Workbenches 返回该用户可见的工作台，顺序稳定。
//
// 用 Scopes 而不是 Permissions(ctx, s, nil)：后者 target 为 nil 时只返回
// 全局授予带来的权限，会把「只在某个节点上持 org_admin」的人算成什么都没有——
// 而那恰恰是平台管理最主要的用户。
func Workbenches(ctx context.Context, r *authz.Resolver, s authz.Subject) ([]string, error) {
	out := make([]string, 0, len(workbenchRules))
	for _, rule := range workbenchRules {
		if len(rule.requires) == 0 {
			out = append(out, rule.id)
			continue
		}
		ok, err := holdsAnywhere(ctx, r, s, rule.requires)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rule.id)
		}
	}
	return out, nil
}

// holdsAnywhere 判断主体是否在任意位置（全局授予或任一节点）持有其中一条权限。
func holdsAnywhere(
	ctx context.Context, r *authz.Resolver, s authz.Subject, perms []string,
) (bool, error) {
	for _, p := range perms {
		global, nodes, err := r.Scopes(ctx, s, p)
		if err != nil {
			return false, err
		}
		if global || len(nodes) > 0 {
			return true, nil
		}
	}
	return false, nil
}
