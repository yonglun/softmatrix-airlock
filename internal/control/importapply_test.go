package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyImportCreatesAddedNodes(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	items := []DiffItem{
		{Kind: DiffAdded, ExternalID: "ou=rd", Name: "研发中心"},
		{Kind: DiffAdded, ExternalID: "ou=plat", ParentExternalID: "ou=rd", Name: "平台产品部"},
	}

	res, err := ApplyImport(ctx, s, "ldap", items)
	require.NoError(t, err)
	require.Equal(t, 2, res.Created)

	rd, err := s.ByExternal(ctx, "ldap", "ou=rd")
	require.NoError(t, err)
	require.Equal(t, "研发中心", rd.Name)

	plat, err := s.ByExternal(ctx, "ldap", "ou=plat")
	require.NoError(t, err)
	require.NotNil(t, plat.ParentID)
	require.Equal(t, rd.ID, *plat.ParentID, "父子关系必须按 ParentExternalID 建立")
	require.Equal(t, rd.Path+"/"+plat.ID, plat.Path)
}

func TestApplyImportCreatesParentBeforeChildRegardlessOfOrder(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	// 子节点排在父节点前面，实现必须自己排好序
	items := []DiffItem{
		{Kind: DiffAdded, ExternalID: "ou=leaf", ParentExternalID: "ou=mid", Name: "小组"},
		{Kind: DiffAdded, ExternalID: "ou=mid", ParentExternalID: "ou=top", Name: "中层"},
		{Kind: DiffAdded, ExternalID: "ou=top", Name: "顶层"},
	}

	res, err := ApplyImport(ctx, s, "ldap", items)
	require.NoError(t, err)
	require.Equal(t, 3, res.Created)

	leaf, err := s.ByExternal(ctx, "ldap", "ou=leaf")
	require.NoError(t, err)
	top, err := s.ByExternal(ctx, "ldap", "ou=top")
	require.NoError(t, err)
	mid, err := s.ByExternal(ctx, "ldap", "ou=mid")
	require.NoError(t, err)

	require.Equal(t, "/"+top.ID+"/"+mid.ID+"/"+leaf.ID, leaf.Path)
}

func TestApplyImportRenames(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, &Org{
		ID: "o1", Name: "技术中心",
		ExternalSource: strp("ldap"), ExternalID: strp("ou=rd"),
	}))

	res, err := ApplyImport(ctx, s, "ldap", []DiffItem{
		{Kind: DiffRenamed, ExternalID: "ou=rd", Name: "研发中心", OrgID: "o1"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Renamed)

	got, err := s.Get(ctx, "o1")
	require.NoError(t, err)
	require.Equal(t, "研发中心", got.Name)
}

func TestApplyImportNeverDeletesOnMissing(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, &Org{
		ID: "o1", Name: "撤销的部门",
		ExternalSource: strp("ldap"), ExternalID: strp("ou=gone"),
	}))

	res, err := ApplyImport(ctx, s, "ldap", []DiffItem{
		{Kind: DiffMissing, ExternalID: "ou=gone", OrgID: "o1", CurrentName: "撤销的部门"},
	})
	require.NoError(t, err)
	require.Equal(t, 0, res.Created)
	require.Equal(t, 0, res.Renamed)
	require.Equal(t, 1, res.MarkedOrphan)

	got, err := s.Get(ctx, "o1")
	require.NoError(t, err, "消失的节点绝不能被删除，只标记")
	require.Equal(t, "撤销的部门", got.Name)
}

func TestApplyImportIsIdempotent(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	items := []DiffItem{{Kind: DiffAdded, ExternalID: "ou=rd", Name: "研发中心"}}

	_, err := ApplyImport(ctx, s, "ldap", items)
	require.NoError(t, err)

	// 第二次应用同样的"新增"——节点已存在，应跳过而不是报错或建重复行
	res, err := ApplyImport(ctx, s, "ldap", items)
	require.NoError(t, err)
	require.Equal(t, 0, res.Created)
	require.Equal(t, 1, res.Skipped)

	all, err := s.All(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1, "重复导入不得产生重复节点")
}

func TestApplyImportEmptyIsNoop(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)

	res, err := ApplyImport(context.Background(), s, "ldap", nil)
	require.NoError(t, err)
	require.Zero(t, res.Created)
	require.Zero(t, res.Renamed)
	require.Zero(t, res.MarkedOrphan)
}
