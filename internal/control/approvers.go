package control

import (
	"context"
	"strings"

	"github.com/softmatrix/airlock/internal/authz"
)

// ApproversOf 返回能审批该节点上申请的活跃用户。
//
// 「能审批」= 持有含 key:write 的角色，且授予落在该节点的祖先链上任意一点，
// 或者是全局授予。既有的 ListGrantsForOrg 只返回落在该节点上的授予——
// 祖先继承与全局授予都不覆盖，而这两种都能审批。
//
// 祖先链从物化路径切段拿到，不额外查库。切段而不是字符串前缀匹配是关键：
// 路径 /root/rd 是 /root/rd2 的前缀，但 rd2 并不是 rd 的祖先。
func ApproversOf(
	ctx context.Context, orgs OrgStore, rbac RBACStore, users UserStore, orgID string,
) ([]*User, error) {
	org, err := orgs.Get(ctx, orgID)
	if err != nil {
		return nil, err
	}

	grants := []RoleGrant{}
	for _, id := range ancestorIDs(org.Path) {
		g, err := rbac.ListGrantsForOrg(ctx, id)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g...)
	}
	global, err := rbac.ListGlobalGrants(ctx)
	if err != nil {
		return nil, err
	}
	grants = append(grants, global...)

	seen := map[string]bool{}
	out := []*User{}
	for _, g := range grants {
		if seen[g.UserID] || !roleGrantsKeyWrite(g.RoleID) {
			continue
		}
		seen[g.UserID] = true

		u, err := users.ByID(ctx, g.UserID)
		if err != nil {
			// 授予指向一个查不到的用户不该让整条通知链断掉。
			continue
		}
		if u.Status != UserStatusActive {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

// ancestorIDs 把物化路径 /root/rd/gw 切成 [root rd gw]。
// 节点自身也算在内——在自己节点上的授予当然能审批自己的申请。
func ancestorIDs(path string) []string {
	out := []string{}
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// roleGrantsKeyWrite 判断某角色是否含 key:write。
// 权限集的真相来源是 authz 注册表，不是数据库。
func roleGrantsKeyWrite(roleID string) bool {
	for _, r := range authz.BuiltinRoles() {
		if r.ID != roleID {
			continue
		}
		for _, p := range r.Permissions {
			if p == authz.PermKeyWrite {
				return true
			}
		}
		return false
	}
	return false
}
