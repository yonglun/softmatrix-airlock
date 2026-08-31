package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/litellm"
)

// tree 造一棵测试用的组织树。path 直接给定，避免依赖存储层。
func tree() []*Org {
	return []*Org{
		{ID: "root", Name: "集团", Path: "/root"},
		{ID: "rd", Name: "研发中心", Path: "/root/rd"},
		{ID: "rd2", Name: "销售部", Path: "/root/rd2"},
		{ID: "plat", Name: "平台产品部", Path: "/root/rd/plat"},
		{ID: "gw", Name: "网关组", Path: "/root/rd/plat/gw", IsKeyHolder: true},
	}
}

func TestDesiredOrgsAreDepthTwoNodes(t *testing.T) {
	got := DesiredStateFrom(tree())
	require.Equal(t, []litellm.Organization{
		{ID: "rd", Alias: "研发中心"},
		{ID: "rd2", Alias: "销售部"},
	}, got.Orgs, "只有 depth==2 的节点成为 Organization")
}

func TestDesiredTeamsComeFromKeyHolders(t *testing.T) {
	got := DesiredStateFrom(tree())
	require.Equal(t, []litellm.Team{
		{ID: "gw", Alias: "网关组", OrganizationID: strp("rd")},
	}, got.Teams, "深层 key holder 挂到它的 depth-2 祖先上")
}

func TestDesiredIgnoresRootAndMiddleLayers(t *testing.T) {
	got := DesiredStateFrom(tree())
	for _, o := range got.Orgs {
		require.NotEqual(t, "root", o.ID, "根节点不建实体")
		require.NotEqual(t, "plat", o.ID, "中间层不建实体")
	}
	require.Len(t, got.Teams, 1, "未标记的节点不建 Team")
}

func TestDesiredDepthTwoKeyHolderIsBothOrgAndTeam(t *testing.T) {
	// 一个业务线自己就是预算边界。已实测：同 ID 既做 Organization 又做 Team、
	// 且 Team 挂在自己身上，在 LiteLLM 侧完全合法（两张表互不冲突）。
	orgs := []*Org{
		{ID: "root", Name: "集团", Path: "/root"},
		{ID: "biz", Name: "业务线", Path: "/root/biz", IsKeyHolder: true},
	}
	got := DesiredStateFrom(orgs)

	require.Equal(t, []litellm.Organization{{ID: "biz", Alias: "业务线"}}, got.Orgs)
	require.Equal(t, []litellm.Team{
		{ID: "biz", Alias: "业务线", OrganizationID: strp("biz")},
	}, got.Teams)
}

func TestDesiredRootKeyHolderHasNoOrganization(t *testing.T) {
	// 根节点被标记时没有 depth-2 祖先，其 Team 不挂任何 Organization。
	// 代价是它头上没有组织级预算天花板——这是真实的能力缺口，不是 bug。
	orgs := []*Org{{ID: "solo", Name: "独立公司", Path: "/solo", IsKeyHolder: true}}
	got := DesiredStateFrom(orgs)

	require.Empty(t, got.Orgs, "depth-1 节点不建 Organization")
	require.Equal(t, []litellm.Team{
		{ID: "solo", Alias: "独立公司", OrganizationID: nil},
	}, got.Teams)
}

func TestDesiredHandlesMultipleRoots(t *testing.T) {
	// organizations 表对 parent_id IS NULL 没有唯一约束，多根是合法状态。
	orgs := []*Org{
		{ID: "r1", Name: "集团一", Path: "/r1"},
		{ID: "r2", Name: "集团二", Path: "/r2"},
		{ID: "a", Name: "A 事业部", Path: "/r1/a"},
		{ID: "b", Name: "B 事业部", Path: "/r2/b"},
	}
	got := DesiredStateFrom(orgs)

	require.Equal(t, []litellm.Organization{
		{ID: "a", Alias: "A 事业部"},
		{ID: "b", Alias: "B 事业部"},
	}, got.Orgs, "各根的子节点各自成为 Organization")
}

func TestDesiredIsSortedForStability(t *testing.T) {
	// 顺序稳定，否则对账日志与状态接口每轮都在抖。
	shuffled := []*Org{
		{ID: "z", Name: "Z", Path: "/root/z"},
		{ID: "a", Name: "A", Path: "/root/a"},
		{ID: "root", Name: "集团", Path: "/root"},
	}
	got := DesiredStateFrom(shuffled)
	require.Equal(t, "a", got.Orgs[0].ID)
	require.Equal(t, "z", got.Orgs[1].ID)
}

func TestDesiredEmptyTreeIsEmptyNotNil(t *testing.T) {
	got := DesiredStateFrom(nil)
	require.NotNil(t, got.Orgs)
	require.NotNil(t, got.Teams)
	require.Empty(t, got.Orgs)
	require.Empty(t, got.Teams)
}

// seedTree 把 tree() 那棵树按父子顺序写进 store。
//
// 走 Create 而不是直接塞内部 map：fakeOrgStore 带互斥锁，
// 而 Task 9 的 Run 测试会在另一个 goroutine 里读它——直接写 map 会在 -race 下炸。
// Create 自己按父节点算 path，因此这里只给 ParentID，不给 Path。
func seedTree(t *testing.T, store *fakeOrgStore) {
	t.Helper()
	ctx := context.Background()
	root, rd, plat := "root", "rd", "plat"
	for _, o := range []*Org{
		{ID: "root", Name: "集团"},
		{ID: "rd", Name: "研发中心", ParentID: &root},
		{ID: "rd2", Name: "销售部", ParentID: &root},
		{ID: "plat", Name: "平台产品部", ParentID: &rd},
		{ID: "gw", Name: "网关组", ParentID: &plat, IsKeyHolder: true},
	} {
		require.NoError(t, store.Create(ctx, o))
	}
}

// syncFixture 造一个用上面那棵树当组织树的同步器。
func syncFixture(t *testing.T) (*Syncer, *fakeOrgStore, *fakeLiteLLM) {
	t.Helper()
	store := newFakeOrgStore()
	seedTree(t, store)
	admin := newFakeLiteLLM()
	return NewSyncer(SyncerDeps{Orgs: store, Admin: admin}), store, admin
}

func TestPlanReportsEverythingMissingOnEmptyUpstream(t *testing.T) {
	s, _, _ := syncFixture(t)

	plan, err := s.Plan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.MissingOrgs, 2)
	require.Len(t, plan.MissingTeams, 1)
	require.Empty(t, plan.MismatchedOrgs)
	require.Empty(t, plan.ExtraOrgs)
}

func TestPlanReportsNothingWhenInSync(t *testing.T) {
	s, _, admin := syncFixture(t)
	admin.orgs["rd"] = litellm.Organization{ID: "rd", Alias: "研发中心"}
	admin.orgs["rd2"] = litellm.Organization{ID: "rd2", Alias: "销售部"}
	admin.teams["gw"] = litellm.Team{ID: "gw", Alias: "网关组", OrganizationID: strp("rd")}

	plan, err := s.Plan(context.Background())
	require.NoError(t, err)
	require.True(t, plan.InSync(), "两侧一致时计划应为空")
}

func TestPlanDetectsAliasDrift(t *testing.T) {
	s, _, admin := syncFixture(t)
	admin.orgs["rd"] = litellm.Organization{ID: "rd", Alias: "被人改过的名字"}
	admin.orgs["rd2"] = litellm.Organization{ID: "rd2", Alias: "销售部"}
	admin.teams["gw"] = litellm.Team{ID: "gw", Alias: "网关组", OrganizationID: strp("rd")}

	plan, err := s.Plan(context.Background())
	require.NoError(t, err)
	require.Equal(t, []litellm.Organization{{ID: "rd", Alias: "研发中心"}}, plan.MismatchedOrgs)
}

func TestPlanDetectsTeamRehomed(t *testing.T) {
	// 有人在 LiteLLM 侧把团队挂到了别的组织下，对账要把它改回来。
	s, _, admin := syncFixture(t)
	admin.orgs["rd"] = litellm.Organization{ID: "rd", Alias: "研发中心"}
	admin.orgs["rd2"] = litellm.Organization{ID: "rd2", Alias: "销售部"}
	admin.teams["gw"] = litellm.Team{ID: "gw", Alias: "网关组", OrganizationID: strp("rd2")}

	plan, err := s.Plan(context.Background())
	require.NoError(t, err)
	require.Len(t, plan.MismatchedTeams, 1)
	require.Equal(t, "rd", *plan.MismatchedTeams[0].OrganizationID)
}

func TestPlanReportsExtraButNeverPlansDeletion(t *testing.T) {
	// LiteLLM 侧多出来的实体只报告、不删除——删 Organization 会级联
	// 删光其下全部 Team，而那些 Team 上可能绑着在用的 Key。
	s, _, admin := syncFixture(t)
	admin.orgs["stranger"] = litellm.Organization{ID: "stranger", Alias: "别人建的"}
	admin.teams["stranger-team"] = litellm.Team{ID: "stranger-team", Alias: "别人的团队"}

	plan, err := s.Plan(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"stranger"}, plan.ExtraOrgs)
	require.Equal(t, []string{"stranger-team"}, plan.ExtraTeams)
}

func TestPlanAbortsWhenListOrganizationsFails(t *testing.T) {
	// 绝不能因为「查不到」就当成「不存在」——那会对已存在的实体重复创建。
	s, _, admin := syncFixture(t)
	admin.listOrgsErr = errors.New("上游不可用")

	_, err := s.Plan(context.Background())
	require.Error(t, err)
}

func TestPlanAbortsWhenListTeamsFails(t *testing.T) {
	s, _, admin := syncFixture(t)
	admin.listTeamsErr = errors.New("上游不可用")

	_, err := s.Plan(context.Background())
	require.Error(t, err)
}

func TestReconcileCreatesEverythingOnEmptyUpstream(t *testing.T) {
	s, _, admin := syncFixture(t)

	res, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, res.OrgsCreated)
	require.Equal(t, 1, res.TeamsCreated)
	require.Contains(t, admin.orgs, "rd")
	require.Contains(t, admin.teams, "gw")
	require.Equal(t, "rd", *admin.teams["gw"].OrganizationID)
}

func TestReconcileCreatesOrganizationsBeforeTeams(t *testing.T) {
	// 上游对挂到不存在 organization_id 的 Team 会 400 拒绝，
	// 所以顺序不是风格问题，是硬约束。
	s, _, admin := syncFixture(t)

	_, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)

	lastOrg, firstTeam := -1, -1
	for i, c := range admin.calls {
		if strings.HasPrefix(c, "create-org:") {
			lastOrg = i
		}
		if strings.HasPrefix(c, "create-team:") && firstTeam < 0 {
			firstTeam = i
		}
	}
	require.Greater(t, firstTeam, lastOrg, "所有组织必须先于团队创建")
}

func TestReconcileIsIdempotent(t *testing.T) {
	s, _, admin := syncFixture(t)

	_, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)
	admin.calls = nil

	res, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, res.OrgsCreated)
	require.Zero(t, res.TeamsCreated)
	require.Empty(t, admin.calls, "已经一致时不该再发任何写请求")
}

func TestReconcileFixesDrift(t *testing.T) {
	s, _, admin := syncFixture(t)
	_, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)

	// 有人在 LiteLLM 侧改了名字、又把团队挂到别的组织下。
	admin.orgs["rd"] = litellm.Organization{ID: "rd", Alias: "被改过"}
	admin.teams["gw"] = litellm.Team{ID: "gw", Alias: "也被改过", OrganizationID: strp("rd2")}

	res, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.OrgsUpdated)
	require.Equal(t, 1, res.TeamsUpdated)
	require.Equal(t, "研发中心", admin.orgs["rd"].Alias)
	require.Equal(t, "网关组", admin.teams["gw"].Alias)
	require.Equal(t, "rd", *admin.teams["gw"].OrganizationID)
}

func TestReconcileNeverDeletesExtraEntities(t *testing.T) {
	s, _, admin := syncFixture(t)
	admin.orgs["stranger"] = litellm.Organization{ID: "stranger", Alias: "别人建的"}

	res, err := s.ReconcileOnce(context.Background())
	require.NoError(t, err)
	require.Contains(t, admin.orgs, "stranger", "多出来的实体绝不删除")
	require.Equal(t, []string{"stranger"}, res.ExtraOrgs)
	for _, c := range admin.calls {
		require.False(t, strings.HasPrefix(c, "delete-"), "对账不该发任何删除请求：%s", c)
	}
}

func TestReconcileOneFailureDoesNotBlockOthers(t *testing.T) {
	// 各节点彼此独立，一个坏节点不该把整棵树卡住。
	// 这一条与离职对账相反——那里的几步是有序且安全攸关的。
	s, _, admin := syncFixture(t)
	admin.failCreateOrg["rd"] = true

	res, err := s.ReconcileOnce(context.Background())
	require.Error(t, err, "整体仍然报错，让调用方知道这轮没干净")
	require.Contains(t, admin.orgs, "rd2", "其余组织照常创建")
	require.Len(t, res.Errors, 1)
}

func TestReconcileSkipsTeamWhoseOrgFailed(t *testing.T) {
	// rd 建不出来，挂在它下面的 gw 就不该硬撞上游的 400，
	// 而应该被跳过并说明原因，下一轮再试。
	s, _, admin := syncFixture(t)
	admin.failCreateOrg["rd"] = true

	res, err := s.ReconcileOnce(context.Background())
	require.Error(t, err)
	require.NotContains(t, admin.teams, "gw")
	require.Equal(t, 1, res.Skipped)
	for _, c := range admin.calls {
		require.NotEqual(t, "create-team:gw", c, "不该对缺失组织的团队发创建请求")
	}
}

func TestReconcileAbortsEntirelyWhenListFails(t *testing.T) {
	s, _, admin := syncFixture(t)
	admin.listOrgsErr = errors.New("上游不可用")

	_, err := s.ReconcileOnce(context.Background())
	require.Error(t, err)
	require.Empty(t, admin.calls, "拉取失败时不该发出任何写请求")
}
