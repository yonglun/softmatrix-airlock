package control

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func extOrg(id, name, extID string) *Org {
	src := "ldap"
	return &Org{ID: id, Name: name, ExternalSource: &src, ExternalID: &extID}
}

func findDiff(items []DiffItem, extID string) (DiffItem, bool) {
	for _, it := range items {
		if it.ExternalID == extID {
			return it, true
		}
	}
	return DiffItem{}, false
}

func TestComputeDiffDetectsAdded(t *testing.T) {
	remote := []ExternalOrgNode{
		{ExternalID: "ou=rd", Name: "研发中心"},
		{ExternalID: "ou=plat", ParentExternalID: "ou=rd", Name: "平台产品部"},
	}
	items := ComputeDiff(remote, nil, "ldap")

	require.Len(t, items, 2)
	for _, it := range items {
		require.Equal(t, DiffAdded, it.Kind)
		require.True(t, it.DefaultSelected, "新增是纯增量，默认勾选")
	}

	plat, ok := findDiff(items, "ou=plat")
	require.True(t, ok)
	require.Equal(t, "ou=rd", plat.ParentExternalID)
}

func TestComputeDiffDetectsRenamedAndDoesNotSelectByDefault(t *testing.T) {
	remote := []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}
	local := []*Org{extOrg("o1", "技术中心", "ou=rd")}

	items := ComputeDiff(remote, local, "ldap")
	require.Len(t, items, 1)

	it := items[0]
	require.Equal(t, DiffRenamed, it.Kind)
	require.Equal(t, "研发中心", it.Name, "Name 是 IdP 侧的新名字")
	require.Equal(t, "技术中心", it.CurrentName, "CurrentName 是 Airlock 侧现名")
	require.Equal(t, "o1", it.OrgID)
	require.False(t, it.DefaultSelected,
		"Airlock 侧可能是故意改的名，不该被 HR 系统默认覆盖")
}

func TestComputeDiffDetectsMissingAndDoesNotSelectByDefault(t *testing.T) {
	local := []*Org{extOrg("o1", "已撤销的部门", "ou=gone")}

	items := ComputeDiff(nil, local, "ldap")
	require.Len(t, items, 1)

	it := items[0]
	require.Equal(t, DiffMissing, it.Kind)
	require.Equal(t, "已撤销的部门", it.CurrentName)
	require.Equal(t, "o1", it.OrgID)
	require.False(t, it.DefaultSelected,
		"消失绝不默认删除——节点下可能挂着 Key、账单和子节点")
}

func TestComputeDiffIgnoresUnchanged(t *testing.T) {
	remote := []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}
	local := []*Org{extOrg("o1", "研发中心", "ou=rd")}

	require.Empty(t, ComputeDiff(remote, local, "ldap"),
		"名字一致的节点不该出现在差异列表里")
}

func TestComputeDiffIgnoresManualNodes(t *testing.T) {
	// 手工建的节点 external_source 为 NULL，不属于任何 IdP 来源，
	// 不能因为 IdP 里没有它就报告为"消失"。
	local := []*Org{
		{ID: "m1", Name: "跨部门项目组"},
		extOrg("o1", "研发中心", "ou=rd"),
	}
	remote := []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}

	require.Empty(t, ComputeDiff(remote, local, "ldap"),
		"手工节点必须完全不受导入影响")
}

func TestComputeDiffIgnoresOtherSources(t *testing.T) {
	// 从钉钉导入的节点，不该在 LDAP 导入时被判为"消失"
	src := "dingtalk"
	dingID := "dept-1"
	local := []*Org{{ID: "d1", Name: "钉钉部门", ExternalSource: &src, ExternalID: &dingID}}

	require.Empty(t, ComputeDiff(nil, local, "ldap"),
		"只该比较同一来源的节点")
}

func TestComputeDiffMixedScenario(t *testing.T) {
	remote := []ExternalOrgNode{
		{ExternalID: "ou=rd", Name: "研发中心"},    // 未变
		{ExternalID: "ou=plat", Name: "平台产品部"}, // 改名
		{ExternalID: "ou=new", Name: "新部门"},    // 新增
	}
	local := []*Org{
		extOrg("o1", "研发中心", "ou=rd"),
		extOrg("o2", "平台部", "ou=plat"),
		extOrg("o3", "旧部门", "ou=gone"), // 消失
	}

	items := ComputeDiff(remote, local, "ldap")
	require.Len(t, items, 3)

	added, ok := findDiff(items, "ou=new")
	require.True(t, ok)
	require.Equal(t, DiffAdded, added.Kind)
	require.True(t, added.DefaultSelected)

	renamed, ok := findDiff(items, "ou=plat")
	require.True(t, ok)
	require.Equal(t, DiffRenamed, renamed.Kind)
	require.False(t, renamed.DefaultSelected)

	missing, ok := findDiff(items, "ou=gone")
	require.True(t, ok)
	require.Equal(t, DiffMissing, missing.Kind)
	require.False(t, missing.DefaultSelected)
}

func TestComputeDiffIsDeterministic(t *testing.T) {
	remote := []ExternalOrgNode{
		{ExternalID: "ou=c", Name: "C"},
		{ExternalID: "ou=a", Name: "A"},
		{ExternalID: "ou=b", Name: "B"},
	}

	first := ComputeDiff(remote, nil, "ldap")
	for i := 0; i < 20; i++ {
		require.Equal(t, first, ComputeDiff(remote, nil, "ldap"),
			"同样输入必须产出同样顺序，否则预览页面每次刷新都在跳")
	}
}

func TestComputeDiffReturnsEmptySliceNotNil(t *testing.T) {
	// 复审第 6 条：nil slice 被 encoding/json 编码成 null，
	// 前端写 preview.items.length 会直接抛异常。
	// 幂等无变化是这个接口最常见的稳态返回，必须是 []。
	remote := []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}
	local := []*Org{extOrg("o1", "研发中心", "ou=rd")}

	got := ComputeDiff(remote, local, "ldap")
	require.NotNil(t, got, "空差异必须是 []DiffItem{} 而不是 nil")
	require.Empty(t, got)

	encoded, err := json.Marshal(map[string]any{"items": got})
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[]}`, string(encoded))
}
