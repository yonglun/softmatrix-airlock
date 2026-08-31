package control

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/softmatrix/airlock/internal/litellm"
)

// DesiredState 是 Airlock 组织树在 LiteLLM 侧应有的样子。
type DesiredState struct {
	Orgs  []litellm.Organization
	Teams []litellm.Team
}

// pathSegments 把物化路径切成节点 ID 序列。路径形如 /id1/id2/id3。
func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// DesiredStateFrom 从组织树算出期望态。纯函数，不碰数据库也不碰网络。
//
// 映射规则（见设计文档 §4）：
//   - depth 恰好为 2 的节点 → Organization
//   - is_key_holder 的节点（任意深度）→ Team，挂到其 path 第 2 段对应的 Organization
//   - 根层与中间层不建实体
//
// 结果按 ID 排序，保证每轮对账的日志与状态输出稳定可比。
func DesiredStateFrom(orgs []*Org) DesiredState {
	out := DesiredState{
		Orgs:  []litellm.Organization{},
		Teams: []litellm.Team{},
	}

	for _, o := range orgs {
		segs := pathSegments(o.Path)

		if len(segs) == 2 {
			out.Orgs = append(out.Orgs, litellm.Organization{ID: o.ID, Alias: o.Name})
		}

		if !o.IsKeyHolder {
			continue
		}
		var orgID *string
		if len(segs) >= 2 {
			// 第 2 段就是该节点所属的 depth-2 祖先。节点自身是 depth-2 时
			// 这里取到的是它自己——已实测：同 ID 既做 Organization 又做 Team 合法。
			id := segs[1]
			orgID = &id
		}
		out.Teams = append(out.Teams, litellm.Team{
			ID: o.ID, Alias: o.Name, OrganizationID: orgID,
		})
	}

	sort.Slice(out.Orgs, func(i, j int) bool { return out.Orgs[i].ID < out.Orgs[j].ID })
	sort.Slice(out.Teams, func(i, j int) bool { return out.Teams[i].ID < out.Teams[j].ID })
	return out
}

// LiteLLMAdmin 是同步所需的上游管理能力。
// 定义在 control 侧、由 internal/litellm 实现——依赖方向单向。
type LiteLLMAdmin interface {
	ListOrganizations(ctx context.Context) ([]litellm.Organization, error)
	CreateOrganization(ctx context.Context, o litellm.Organization) error
	UpdateOrganization(ctx context.Context, o litellm.Organization) error
	DeleteOrganization(ctx context.Context, id string) error

	ListTeams(ctx context.Context) ([]litellm.Team, error)
	CreateTeam(ctx context.Context, t litellm.Team) error
	UpdateTeam(ctx context.Context, t litellm.Team) error
	DeleteTeam(ctx context.Context, id string) error
}

type SyncerDeps struct {
	Orgs  OrgStore
	Admin LiteLLMAdmin
}

type Syncer struct {
	deps    SyncerDeps
	trigger chan struct{}
}

func NewSyncer(deps SyncerDeps) *Syncer {
	return &Syncer{deps: deps, trigger: make(chan struct{}, 1)}
}

// SyncPlan 是一轮对账要做的事。只描述、不执行。
type SyncPlan struct {
	MissingOrgs     []litellm.Organization
	MismatchedOrgs  []litellm.Organization
	MissingTeams    []litellm.Team
	MismatchedTeams []litellm.Team
	// ExtraOrgs / ExtraTeams 是 LiteLLM 侧多出来的实体。只报告，绝不删除。
	ExtraOrgs  []string
	ExtraTeams []string
	// ExistingOrgs 是期望态里已经存在于上游的组织 ID。
	// ReconcileOnce 用它判断团队能不能挂上去，从而不必再拉一次组织列表。
	ExistingOrgs []string
}

// InSync 表示两侧已经一致（多出来的实体不算不一致——我们本就不管它们）。
func (p SyncPlan) InSync() bool {
	return len(p.MissingOrgs) == 0 && len(p.MismatchedOrgs) == 0 &&
		len(p.MissingTeams) == 0 && len(p.MismatchedTeams) == 0
}

func sameOrgPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Plan 拉取两侧现状并算出差异。只读，不做任何写入。
//
// 拉取现状失败时整轮中止：绝不能因为「查不到」就当成「不存在」，
// 那会对已经存在的实体重复创建。
func (s *Syncer) Plan(ctx context.Context) (SyncPlan, error) {
	var plan SyncPlan

	local, err := s.deps.Orgs.All(ctx)
	if err != nil {
		return plan, fmt.Errorf("查询组织树失败: %w", err)
	}
	want := DesiredStateFrom(local)

	haveOrgs, err := s.deps.Admin.ListOrganizations(ctx)
	if err != nil {
		return plan, fmt.Errorf("拉取 LiteLLM 组织列表失败: %w", err)
	}
	haveTeams, err := s.deps.Admin.ListTeams(ctx)
	if err != nil {
		return plan, fmt.Errorf("拉取 LiteLLM 团队列表失败: %w", err)
	}

	orgByID := make(map[string]litellm.Organization, len(haveOrgs))
	for _, o := range haveOrgs {
		orgByID[o.ID] = o
	}
	teamByID := make(map[string]litellm.Team, len(haveTeams))
	for _, t := range haveTeams {
		teamByID[t.ID] = t
	}

	wantOrgIDs := map[string]bool{}
	for _, w := range want.Orgs {
		wantOrgIDs[w.ID] = true
		cur, ok := orgByID[w.ID]
		if !ok {
			plan.MissingOrgs = append(plan.MissingOrgs, w)
			continue
		}
		// 已存在。名字不符的仍然要改，但它已经能承载团队了。
		plan.ExistingOrgs = append(plan.ExistingOrgs, w.ID)
		if cur.Alias != w.Alias {
			plan.MismatchedOrgs = append(plan.MismatchedOrgs, w)
		}
	}

	wantTeamIDs := map[string]bool{}
	for _, w := range want.Teams {
		wantTeamIDs[w.ID] = true
		cur, ok := teamByID[w.ID]
		switch {
		case !ok:
			plan.MissingTeams = append(plan.MissingTeams, w)
		case cur.Alias != w.Alias || !sameOrgPtr(cur.OrganizationID, w.OrganizationID):
			plan.MismatchedTeams = append(plan.MismatchedTeams, w)
		}
	}

	for _, o := range haveOrgs {
		if !wantOrgIDs[o.ID] {
			plan.ExtraOrgs = append(plan.ExtraOrgs, o.ID)
		}
	}
	for _, t := range haveTeams {
		if !wantTeamIDs[t.ID] {
			plan.ExtraTeams = append(plan.ExtraTeams, t.ID)
		}
	}
	sort.Strings(plan.ExtraOrgs)
	sort.Strings(plan.ExtraTeams)
	return plan, nil
}

// SyncResult 汇总一轮对账的结果。
type SyncResult struct {
	OrgsCreated  int
	OrgsUpdated  int
	TeamsCreated int
	TeamsUpdated int
	// Skipped 是因所属组织不存在而本轮跳过的团队数。
	Skipped int
	// ExtraOrgs / ExtraTeams 是 LiteLLM 侧多出来、未被触碰的实体。
	ExtraOrgs  []string
	ExtraTeams []string
	// Errors 是单个实体的失败原因。不为空时 ReconcileOnce 整体也返回错误。
	Errors []string
}

// ReconcileOnce 跑一轮对账：补建缺失的、纠正不一致的，绝不删除多出来的。
//
// 单个实体失败只记录并继续处理其余实体——各节点彼此独立，
// 一个坏节点不该把整棵树卡住，失败项下一轮自动重试。
func (s *Syncer) ReconcileOnce(ctx context.Context) (SyncResult, error) {
	var res SyncResult

	plan, err := s.Plan(ctx)
	if err != nil {
		return res, err
	}
	res.ExtraOrgs, res.ExtraTeams = plan.ExtraOrgs, plan.ExtraTeams

	// 已经在上游存在的组织 = Plan 记下的 ExistingOrgs + 本轮成功建出来的。
	// 不再多发一次 ListOrganizations：Plan 刚刚才拉过，重复拉既浪费一次往返，
	// 又给了两次调用之间状态漂移的机会。
	//
	// 团队只会挂到 depth-2 节点上，而 depth-2 节点必然都在期望态里，
	// 所以这个集合足够判断「团队的所属组织是否已存在」。
	liveOrgs := make(map[string]bool, len(plan.ExistingOrgs))
	for _, id := range plan.ExistingOrgs {
		liveOrgs[id] = true
	}

	// ---- 第一阶段：组织 ----
	for _, o := range plan.MissingOrgs {
		if err := s.deps.Admin.CreateOrganization(ctx, o); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("创建组织 %s 失败: %v", o.ID, err))
			continue
		}
		liveOrgs[o.ID] = true
		res.OrgsCreated++
	}
	for _, o := range plan.MismatchedOrgs {
		if err := s.deps.Admin.UpdateOrganization(ctx, o); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("更新组织 %s 失败: %v", o.ID, err))
			continue
		}
		res.OrgsUpdated++
	}

	// ---- 第二阶段：团队（顺序约束）----
	apply := func(list []litellm.Team, act func(context.Context, litellm.Team) error, verb string, n *int) {
		for _, t := range list {
			if t.OrganizationID != nil && !liveOrgs[*t.OrganizationID] {
				res.Skipped++
				slog.Warn("所属组织不存在，本轮跳过该团队",
					"team", t.ID, "organization", *t.OrganizationID)
				continue
			}
			if err := act(ctx, t); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s团队 %s 失败: %v", verb, t.ID, err))
				continue
			}
			*n++
		}
	}
	apply(plan.MissingTeams, s.deps.Admin.CreateTeam, "创建", &res.TeamsCreated)
	apply(plan.MismatchedTeams, s.deps.Admin.UpdateTeam, "更新", &res.TeamsUpdated)

	if len(res.Errors) > 0 {
		return res, fmt.Errorf("本轮同步有 %d 项失败，将在下轮重试", len(res.Errors))
	}
	return res, nil
}
