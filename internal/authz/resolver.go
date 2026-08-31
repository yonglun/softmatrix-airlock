package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrOrgNotFound 由 Store 在目标节点不存在时返回。
var ErrOrgNotFound = errors.New("组织节点不存在")

// Grant 是一条角色授予。OrgID 为 nil 表示全局授予。
type Grant struct {
	RoleID string
	OrgID  *string
}

// Subject 是被判定的主体。与 control.User 解耦，
// 让 authz 包不必知道用户还有 email、显示名之类与判定无关的字段。
type Subject struct {
	UserID       string
	Active       bool
	PrimaryOrgID *string
}

// Store 提供判定所需的全部数据。
type Store interface {
	// GrantsForUser 返回该用户的全部授予。
	GrantsForUser(ctx context.Context, userID string) ([]Grant, error)
	// PermissionsForRole 返回角色的权限集。角色不存在时返回空切片而非错误。
	PermissionsForRole(ctx context.Context, roleID string) ([]string, error)
	// OrgPath 返回节点的物化路径（形如 /id1/id2）。不存在时返回 ErrOrgNotFound。
	OrgPath(ctx context.Context, orgID string) (string, error)
}

// Resolver 是权限判定器。
type Resolver struct {
	store Store
}

func NewResolver(store Store) *Resolver {
	return &Resolver{store: store}
}

// Can 判定主体是否在目标节点上拥有某权限。
// target 为 nil 表示无特定目标节点。
//
// 判定是纯正向并集，没有拒绝规则——任一来源给出该权限即通过。
func (r *Resolver) Can(ctx context.Context, s Subject, permission string, target *string) (bool, error) {
	def, ok := Lookup(permission)
	if !ok {
		return false, fmt.Errorf("未注册的权限: %s", permission)
	}
	if !s.Active {
		return false, nil
	}

	held, err := r.effectivePermissions(ctx, s, def.Scope, target)
	if err != nil {
		return false, err
	}
	return held[permission], nil
}

// effectivePermissions 汇总主体在给定作用域与目标下的全部权限。
func (r *Resolver) effectivePermissions(
	ctx context.Context, s Subject, scope Scope, target *string,
) (map[string]bool, error) {
	grants, err := r.store.GrantsForUser(ctx, s.UserID)
	if err != nil {
		return nil, fmt.Errorf("查询用户授予失败: %w", err)
	}

	// 目标节点的祖先链（含自身）。无目标时为空集。
	ancestors := map[string]bool{}
	if target != nil && scope == ScopeOrg {
		chain, err := r.ancestorChain(ctx, *target)
		if err != nil {
			return nil, err
		}
		ancestors = chain
	}

	held := map[string]bool{}
	for _, g := range grants {
		if !r.grantApplies(g, scope, ancestors) {
			continue
		}
		perms, err := r.store.PermissionsForRole(ctx, g.RoleID)
		if err != nil {
			return nil, fmt.Errorf("查询角色权限失败（role=%s）: %w", g.RoleID, err)
		}
		for _, p := range perms {
			// 全局权限只能由全局授予赋予——这是 D4 的落地点。
			if def, ok := Lookup(p); ok && def.Scope == ScopeGlobal && g.OrgID != nil {
				continue
			}
			held[p] = true
		}
	}

	if err := r.applyImplicitBaseline(ctx, s, scope, target, held); err != nil {
		return nil, err
	}
	return held, nil
}

// applyImplicitBaseline 叠加隐式开发者基线：有归属的活跃用户，
// 在自己 primary_org_id 的子树内自动拥有开发者角色的权限。
//
// 为什么要隐式而不是给每人写一条授予：授予是 (用户, 角色, 节点) 三元组，
// 每条只对一个人生效，「给根节点授予开发者」覆盖不了全公司——
// 那样每个员工入职都要人工授予一次。
//
// 归属节点查不到时静默跳过而不是报错：悬空的归属只该让基线失效，
// 不该把这个用户的显式授予一起拖垮。
func (r *Resolver) applyImplicitBaseline(
	ctx context.Context, s Subject, scope Scope, target *string, held map[string]bool,
) error {
	if s.PrimaryOrgID == nil || target == nil || scope != ScopeOrg {
		return nil
	}

	homePath, err := r.store.OrgPath(ctx, *s.PrimaryOrgID)
	if err != nil {
		if errors.Is(err, ErrOrgNotFound) {
			return nil
		}
		return fmt.Errorf("查询归属节点路径失败: %w", err)
	}
	targetPath, err := r.store.OrgPath(ctx, *target)
	if err != nil {
		return fmt.Errorf("查询目标节点路径失败: %w", err)
	}

	// 加分隔符再比前缀，避免 /root/rd 把同前缀兄弟 /root/rd2 也算进子树。
	if targetPath != homePath && !strings.HasPrefix(targetPath, homePath+"/") {
		return nil
	}

	perms, err := r.store.PermissionsForRole(ctx, RoleDeveloper)
	if err != nil {
		return fmt.Errorf("查询开发者角色权限失败: %w", err)
	}
	for _, p := range perms {
		if def, ok := Lookup(p); ok && def.Scope == ScopeGlobal {
			continue // 基线绝不赋予全局权限
		}
		held[p] = true
	}
	return nil
}

// grantApplies 判断一条授予在本次判定中是否算数。
func (r *Resolver) grantApplies(g Grant, scope Scope, ancestors map[string]bool) bool {
	if g.OrgID == nil {
		return true // 全局授予对任何判定都算数
	}
	if scope == ScopeGlobal {
		return false // 全局权限不看节点级授予
	}
	return ancestors[*g.OrgID]
}

// ancestorChain 把物化路径切成节点 ID 集合，含目标节点自身。
// 路径形如 /id1/id2/id3，因此不需要额外查库就能拿到完整祖先链。
func (r *Resolver) ancestorChain(ctx context.Context, orgID string) (map[string]bool, error) {
	path, err := r.store.OrgPath(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("查询节点路径失败（org=%s）: %w", orgID, err)
	}
	out := map[string]bool{}
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg != "" {
			out[seg] = true
		}
	}
	return out, nil
}

// Permissions 返回主体在目标节点上的全部有效权限。
// target 为 nil 时只统计全局授予赋予的全局权限。
//
// 供 /api/whoami 与需要一次性拿到整个权限集的场景使用——
// 逐条调用 Can 会重复查询授予与路径。
func (r *Resolver) Permissions(ctx context.Context, s Subject, target *string) (map[string]bool, error) {
	if !s.Active {
		return map[string]bool{}, nil
	}
	scope := ScopeGlobal
	if target != nil {
		scope = ScopeOrg
	}
	return r.effectivePermissions(ctx, s, scope, target)
}

// Scopes 返回主体持有某权限的作用域。
//
// global 为 true 表示该权限在全树生效（来自全局授予）；
// 否则 orgIDs 是持有该权限的节点列表，权限覆盖这些节点各自的整棵子树。
//
// 供组织树列表过滤使用：逐个节点调用 Can 会退化成 N 次查询。
func (r *Resolver) Scopes(ctx context.Context, s Subject, permission string) (bool, []string, error) {
	if _, ok := Lookup(permission); !ok {
		return false, nil, fmt.Errorf("未注册的权限: %s", permission)
	}
	if !s.Active {
		return false, nil, nil
	}

	grants, err := r.store.GrantsForUser(ctx, s.UserID)
	if err != nil {
		return false, nil, fmt.Errorf("查询用户授予失败: %w", err)
	}

	global := false
	seen := map[string]bool{}
	var nodes []string

	for _, g := range grants {
		perms, err := r.store.PermissionsForRole(ctx, g.RoleID)
		if err != nil {
			return false, nil, fmt.Errorf("查询角色权限失败（role=%s）: %w", g.RoleID, err)
		}
		if !contains(perms, permission) {
			continue
		}
		if g.OrgID == nil {
			global = true
			continue
		}
		if !seen[*g.OrgID] {
			seen[*g.OrgID] = true
			nodes = append(nodes, *g.OrgID)
		}
	}
	return global, nodes, nil
}

// CanGrant 判定授予者能否在目标节点上授予某个角色。
//
// 规则：被授予角色的权限集必须是授予者在该节点上已持有权限的子集。
// 否则组织管理员可以给自己授予一个含全局权限的角色，
// 绕过「全局权限只能由全局授予赋予」这条限制间接提权。
func (r *Resolver) CanGrant(ctx context.Context, granter Subject, roleID string, target *string) (bool, error) {
	perms, err := r.store.PermissionsForRole(ctx, roleID)
	if err != nil {
		return false, fmt.Errorf("查询角色权限失败（role=%s）: %w", roleID, err)
	}
	if len(perms) == 0 {
		return false, nil // 角色不存在或没有权限，授予它没有意义
	}
	for _, p := range perms {
		ok, err := r.Can(ctx, granter, p, target)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
