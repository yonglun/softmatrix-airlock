package control

import (
	"context"
	"fmt"
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
