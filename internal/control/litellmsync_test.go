package control

import (
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
