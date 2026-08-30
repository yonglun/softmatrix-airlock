package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

// mkOrg 建一个节点并返回它。parentID 传 nil 表示根节点。
func mkOrg(t *testing.T, s OrgStore, id, name string, parentID *string) *Org {
	t.Helper()
	o := &Org{ID: id, Name: name, ParentID: parentID}
	require.NoError(t, s.Create(context.Background(), o))
	return o
}

func TestOrgStoreCreateRootComputesPath(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)

	got, err := s.Get(ctx, "root")
	require.NoError(t, err)
	require.Equal(t, "/root", got.Path)
	require.Nil(t, got.ParentID)
}

func TestOrgStoreCreateChildAppendsToParentPath(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "rd", "研发中心", strp("root"))
	mkOrg(t, s, "plat", "平台产品部", strp("rd"))

	got, err := s.Get(ctx, "plat")
	require.NoError(t, err)
	require.Equal(t, "/root/rd/plat", got.Path)
}

func TestOrgStoreCreateRejectsUnknownParent(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)

	err := s.Create(context.Background(), &Org{ID: "x", Name: "孤儿", ParentID: strp("nope")})
	require.Error(t, err)
}

func TestOrgStoreGetUnknown(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)

	_, err := s.Get(context.Background(), "nope")
	require.ErrorIs(t, err, ErrOrgNotFound)
}

func TestOrgStoreRenameDoesNotChangePath(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "rd", "技术中心", strp("root"))

	require.NoError(t, s.Rename(ctx, "rd", "研发中心"))

	got, err := s.Get(ctx, "rd")
	require.NoError(t, err)
	require.Equal(t, "研发中心", got.Name)
	require.Equal(t, "/root/rd", got.Path, "path 由 ID 拼成，改名不该影响它")
}

func TestOrgStoreRenameUnknown(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)

	require.ErrorIs(t, s.Rename(context.Background(), "nope", "x"), ErrOrgNotFound)
}

func TestOrgStoreChildren(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))
	mkOrg(t, s, "b", "B", strp("root"))
	mkOrg(t, s, "a1", "A1", strp("a"))

	roots, err := s.Children(ctx, nil)
	require.NoError(t, err)
	require.Len(t, roots, 1)
	require.Equal(t, "root", roots[0].ID)

	kids, err := s.Children(ctx, strp("root"))
	require.NoError(t, err)
	require.Len(t, kids, 2)
}

func TestOrgStoreSubtreeIncludesSelfAndDescendants(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "rd", "研发", strp("root"))
	mkOrg(t, s, "plat", "平台部", strp("rd"))
	mkOrg(t, s, "gw", "网关组", strp("plat"))
	mkOrg(t, s, "other", "其他", strp("root"))

	sub, err := s.Subtree(ctx, "rd")
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, o := range sub {
		ids[o.ID] = true
	}
	require.Equal(t, map[string]bool{"rd": true, "plat": true, "gw": true}, ids)
}

func TestOrgStoreSubtreePrefixDoesNotOvermatch(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	// "rd" 与 "rd2" 是兄弟；按前缀匹配时 /root/rd 不能误吞 /root/rd2
	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "rd", "研发", strp("root"))
	mkOrg(t, s, "rd2", "研发二部", strp("root"))

	sub, err := s.Subtree(ctx, "rd")
	require.NoError(t, err)
	require.Len(t, sub, 1)
	require.Equal(t, "rd", sub[0].ID)
}

func TestOrgStoreByExternal(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, &Org{
		ID: "o1", Name: "研发", ExternalSource: strp("ldap"), ExternalID: strp("ou=rd"),
	}))

	got, err := s.ByExternal(ctx, "ldap", "ou=rd")
	require.NoError(t, err)
	require.Equal(t, "o1", got.ID)

	_, err = s.ByExternal(ctx, "ldap", "ou=nope")
	require.ErrorIs(t, err, ErrOrgNotFound)
}

func TestOrgStoreExternalIDIsUniquePerSource(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, &Org{
		ID: "o1", Name: "研发", ExternalSource: strp("ldap"), ExternalID: strp("ou=rd"),
	}))
	err := s.Create(ctx, &Org{
		ID: "o2", Name: "研发副本", ExternalSource: strp("ldap"), ExternalID: strp("ou=rd"),
	})
	require.Error(t, err, "同一来源下 external_id 必须唯一，这是导入幂等的基础")
}

func TestOrgStoreManualNodesIgnoreExternalUnique(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	// 手工建的节点 external_source 为 NULL，不受唯一索引约束，可以有多个
	require.NoError(t, s.Create(ctx, &Org{ID: "m1", Name: "项目组一"}))
	require.NoError(t, s.Create(ctx, &Org{ID: "m2", Name: "项目组二"}))

	all, err := s.All(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestOrgStoreMoveUpdatesSelfAndDescendantPaths(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	//   root
	//   ├── a
	//   │   └── x
	//   │       └── y
	//   └── b
	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))
	mkOrg(t, s, "b", "B", strp("root"))
	mkOrg(t, s, "x", "X", strp("a"))
	mkOrg(t, s, "y", "Y", strp("x"))

	// 把 x（连同 y）从 a 移到 b 下
	require.NoError(t, s.Move(ctx, "x", strp("b")))

	x, err := s.Get(ctx, "x")
	require.NoError(t, err)
	require.Equal(t, "/root/b/x", x.Path)
	require.Equal(t, "b", *x.ParentID)

	y, err := s.Get(ctx, "y")
	require.NoError(t, err)
	require.Equal(t, "/root/b/x/y", y.Path, "后代的 path 必须跟着一起改")
}

func TestOrgStoreMoveToRoot(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))
	mkOrg(t, s, "x", "X", strp("a"))

	require.NoError(t, s.Move(ctx, "a", nil))

	a, err := s.Get(ctx, "a")
	require.NoError(t, err)
	require.Equal(t, "/a", a.Path)
	require.Nil(t, a.ParentID)

	x, err := s.Get(ctx, "x")
	require.NoError(t, err)
	require.Equal(t, "/a/x", x.Path)
}

func TestOrgStoreMoveRejectsIntoOwnSubtree(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))
	mkOrg(t, s, "x", "X", strp("a"))

	require.ErrorIs(t, s.Move(ctx, "a", strp("x")), ErrOrgCycle,
		"把父节点移到自己的后代下会形成环")
}

func TestOrgStoreMoveRejectsOntoItself(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))

	require.ErrorIs(t, s.Move(ctx, "a", strp("a")), ErrOrgCycle)
}

func TestOrgStoreMoveUnknownNode(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)

	require.ErrorIs(t, s.Move(context.Background(), "nope", nil), ErrOrgNotFound)
}

func TestOrgStoreMoveDoesNotTouchSiblings(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresOrgStore(db)
	ctx := context.Background()

	mkOrg(t, s, "root", "集团", nil)
	mkOrg(t, s, "a", "A", strp("root"))
	mkOrg(t, s, "b", "B", strp("root"))
	// ab 与 a 同前缀，移动 a 时不能被误改
	mkOrg(t, s, "ab", "AB", strp("root"))

	require.NoError(t, s.Move(ctx, "a", strp("b")))

	ab, err := s.Get(ctx, "ab")
	require.NoError(t, err)
	require.Equal(t, "/root/ab", ab.Path, "同前缀的兄弟节点不该被误改")
}
