package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// fakeOrgStore 是内存组织树，让 API 层可以脱离 Postgres 单测。
type fakeOrgStore struct {
	mu       sync.Mutex
	data     map[string]*Org
	liveKeys map[string]int
}

func newFakeOrgStore() *fakeOrgStore {
	return &fakeOrgStore{data: map[string]*Org{}, liveKeys: map[string]int{}}
}

func (f *fakeOrgStore) Create(_ context.Context, o *Org) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := "/" + o.ID
	if o.ParentID != nil {
		p, ok := f.data[*o.ParentID]
		if !ok {
			return ErrOrgNotFound
		}
		path = p.Path + "/" + o.ID
	}
	o.Path = path
	cp := *o
	f.data[o.ID] = &cp
	return nil
}

func (f *fakeOrgStore) Get(_ context.Context, id string) (*Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.data[id]
	if !ok {
		return nil, ErrOrgNotFound
	}
	cp := *o
	return &cp, nil
}

func (f *fakeOrgStore) Rename(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.data[id]
	if !ok {
		return ErrOrgNotFound
	}
	o.Name = name
	return nil
}

func (f *fakeOrgStore) SetKeyHolder(_ context.Context, id string, v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.data[id]
	if !ok {
		return ErrOrgNotFound
	}
	o.IsKeyHolder = v
	return nil
}

// liveKeys 供测试直接设定「该节点下有几把在用密钥」。
func (f *fakeOrgStore) CountLiveKeys(_ context.Context, orgID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveKeys[orgID], nil
}

func (f *fakeOrgStore) Move(_ context.Context, id string, parentID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.data[id]
	if !ok {
		return ErrOrgNotFound
	}
	if parentID != nil && *parentID == id {
		return ErrOrgCycle
	}
	o.ParentID = parentID
	return nil
}

func (f *fakeOrgStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[id]; !ok {
		return ErrOrgNotFound
	}
	for _, o := range f.data {
		if o.ParentID != nil && *o.ParentID == id {
			return ErrOrgHasChildren
		}
	}
	delete(f.data, id)
	return nil
}

func (f *fakeOrgStore) Children(_ context.Context, parentID *string) ([]*Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Org
	for _, o := range f.data {
		switch {
		case parentID == nil && o.ParentID == nil:
			cp := *o
			out = append(out, &cp)
		case parentID != nil && o.ParentID != nil && *o.ParentID == *parentID:
			cp := *o
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeOrgStore) Subtree(_ context.Context, id string) ([]*Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	self, ok := f.data[id]
	if !ok {
		return nil, ErrOrgNotFound
	}
	var out []*Org
	for _, o := range f.data {
		if o.Path == self.Path || strings.HasPrefix(o.Path, self.Path+"/") {
			cp := *o
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeOrgStore) ByExternal(_ context.Context, source, extID string) (*Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.data {
		if o.ExternalSource != nil && *o.ExternalSource == source &&
			o.ExternalID != nil && *o.ExternalID == extID {
			cp := *o
			return &cp, nil
		}
	}
	return nil, ErrOrgNotFound
}

func (f *fakeOrgStore) All(context.Context) ([]*Org, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Org
	for _, o := range f.data {
		cp := *o
		out = append(out, &cp)
	}
	return out, nil
}

type fakeSource struct {
	nodes []ExternalOrgNode
	err   error
}

func (f *fakeSource) Name() string { return "ldap" }
func (f *fakeSource) FetchOrgTree(context.Context) ([]ExternalOrgNode, error) {
	return f.nodes, f.err
}

func newOrgAPI(t *testing.T) (*OrgAPI, *fakeOrgStore, *fakeSource) {
	t.Helper()
	store := newFakeOrgStore()
	src := &fakeSource{}
	resolver := authz.NewResolver(newFakeRBACStore())
	return NewOrgAPI(store, src, resolver), store, src
}

func TestOrgAPICreate(t *testing.T) {
	api, store, _ := newOrgAPI(t)

	body := strings.NewReader(`{"name":"研发中心"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orgs", body)
	rec := httptest.NewRecorder()
	api.HandleCreate(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var got Org
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotEmpty(t, got.ID)
	require.Equal(t, "研发中心", got.Name)

	_, err := store.Get(context.Background(), got.ID)
	require.NoError(t, err)
}

func TestOrgAPICreateRejectsEmptyName(t *testing.T) {
	api, _, _ := newOrgAPI(t)

	req := httptest.NewRequest(http.MethodPost, "/api/orgs", strings.NewReader(`{"name":"  "}`))
	rec := httptest.NewRecorder()
	api.HandleCreate(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOrgAPIDeleteMapsErrorsToStatus(t *testing.T) {
	api, store, _ := newOrgAPI(t)
	ctx := context.Background()

	require.NoError(t, store.Create(ctx, &Org{ID: "p", Name: "父"}))
	pid := "p"
	require.NoError(t, store.Create(ctx, &Org{ID: "c", Name: "子", ParentID: &pid}))

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/p", nil)
	req.SetPathValue("id", "p")
	rec := httptest.NewRecorder()
	api.HandleDelete(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code,
		"有子节点导致的拒绝应是 409，而不是把数据库错误直接抛出去")
	require.Contains(t, rec.Body.String(), "org_has_children")
}

func TestOrgAPIDeleteUnknownReturns404(t *testing.T) {
	api, _, _ := newOrgAPI(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/nope", nil)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	api.HandleDelete(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestOrgAPIImportPreviewDoesNotMutate(t *testing.T) {
	api, store, src := newOrgAPI(t)
	src.nodes = []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}

	rec := httptest.NewRecorder()
	api.HandleImportPreview(rec, httptest.NewRequest(http.MethodGet, "/api/orgs/import/preview", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Source string     `json:"source"`
		Items  []DiffItem `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "ldap", resp.Source)
	require.Len(t, resp.Items, 1)
	require.Equal(t, DiffAdded, resp.Items[0].Kind)
	require.True(t, resp.Items[0].DefaultSelected)

	all, err := store.All(context.Background())
	require.NoError(t, err)
	require.Empty(t, all, "预览绝不能改动任何数据")
}

func TestOrgAPIImportApply(t *testing.T) {
	api, store, src := newOrgAPI(t)
	// Task 18 起，服务端自己拉通讯录重算差异，客户端只勾选 external_id——
	// 这里要让 fakeSource 真的能拉到 ou=rd，勾选才有意义。
	src.nodes = []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}

	body := `{"external_ids":["ou=rd"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/import/apply", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.HandleImportApply(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var res ImportResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, 1, res.Created)

	_, err := store.ByExternal(context.Background(), "ldap", "ou=rd")
	require.NoError(t, err)
}

func TestOrgAPIImportPreviewSurfacesSourceFailure(t *testing.T) {
	api, _, src := newOrgAPI(t)
	src.err = context.DeadlineExceeded

	rec := httptest.NewRecorder()
	api.HandleImportPreview(rec, httptest.NewRequest(http.MethodGet, "/api/orgs/import/preview", nil))

	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// visibilityFixture 造一棵树与一个带判定器的 OrgAPI。
//
//	root
//	├── rd
//	│   └── gw
//	└── sales
func visibilityFixture(t *testing.T) (*OrgAPI, *fakeOrgStore, *fakeRBACStore) {
	t.Helper()
	store := newFakeOrgStore()
	ctx := context.Background()
	require.NoError(t, store.Create(ctx, &Org{ID: "root", Name: "集团"}))
	require.NoError(t, store.Create(ctx, &Org{ID: "rd", Name: "研发中心", ParentID: strp("root")}))
	require.NoError(t, store.Create(ctx, &Org{ID: "gw", Name: "网关组", ParentID: strp("rd")}))
	require.NoError(t, store.Create(ctx, &Org{ID: "sales", Name: "销售部", ParentID: strp("root")}))

	rbac := newFakeRBACStore()
	rbac.setPath("root", "/root")
	rbac.setPath("rd", "/root/rd")
	rbac.setPath("gw", "/root/rd/gw")
	rbac.setPath("sales", "/root/sales")

	api := NewOrgAPI(store, &fakeSource{}, authz.NewResolver(rbac))
	return api, store, rbac
}

// listAs 以某个用户身份调用 HandleList，返回可见节点名集合。
func listAs(t *testing.T, api *OrgAPI, u *User) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/orgs", nil)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, u))
	rec := httptest.NewRecorder()
	api.HandleList(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []Org
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	var names []string
	for _, o := range got {
		names = append(names, o.Name)
	}
	return names
}

func TestListShowsWholeTreeForGlobalReader(t *testing.T) {
	api, _, rbac := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleAuditor,
	}))

	require.ElementsMatch(t, []string{"集团", "研发中心", "网关组", "销售部"}, listAs(t, api, u))
}

func TestListShowsSubtreeAndAncestorsForScopedGrant(t *testing.T) {
	api, _, rbac := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	// rd 及其子树 gw 是真实作用域；root 是祖先，给出来才能渲染成树。
	// sales 与该用户无关，不该出现。
	require.ElementsMatch(t, []string{"集团", "研发中心", "网关组"}, listAs(t, api, u))
}

func TestListShowsHomeSubtreeForImplicitBaseline(t *testing.T) {
	api, _, _ := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive, PrimaryOrgID: strp("rd")}

	require.ElementsMatch(t, []string{"集团", "研发中心", "网关组"}, listAs(t, api, u))
}

func TestListIsEmptyWithoutAnyReadAccess(t *testing.T) {
	api, _, _ := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive}

	require.Empty(t, listAs(t, api, u))
}

// moveAs 以某用户身份把 nodeID 移到 newParent 之下。
func moveAs(t *testing.T, api *OrgAPI, u *User, nodeID string, newParent *string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"parent_id":null}`
	if newParent != nil {
		body = `{"parent_id":"` + *newParent + `"}`
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/orgs/"+nodeID+"/parent", strings.NewReader(body))
	req.SetPathValue("id", nodeID)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, u))
	rec := httptest.NewRecorder()
	api.HandleMove(rec, req)
	return rec
}

func TestMoveRejectsWhenLackingPermissionOnDestination(t *testing.T) {
	// 只在源节点 rd 上有权限，把 rd 的子节点塞进别人管的 sales 之下——必须拒绝。
	// 否则一个部门管理员能把自己的子树挂到任何部门下面。
	api, _, rbac := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	rec := moveAs(t, api, u, "gw", strp("sales"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_denied")
}

func TestMoveAllowsWhenHoldingBothEnds(t *testing.T) {
	api, _, rbac := visibilityFixture(t)
	u := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("root"),
	}))

	rec := moveAs(t, api, u, "gw", strp("sales"))
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMoveToRootRequiresGlobalGrant(t *testing.T) {
	// 把节点移成根节点：目标为 nil，按边界规则要求全局授予。
	api, _, rbac := visibilityFixture(t)
	scoped := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RoleOrgAdmin, OrgID: strp("root"),
	}))
	require.Equal(t, http.StatusForbidden, moveAs(t, api, scoped, "gw", nil).Code)

	global := &User{ID: "u2", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g2", UserID: "u2", RoleID: authz.RoleOrgAdmin,
	}))
	require.Equal(t, http.StatusNoContent, moveAs(t, api, global, "gw", nil).Code)
}

// applyAs 以某用户身份提交导入选择。
func applyAs(t *testing.T, api *OrgAPI, u *User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orgs/import/apply", strings.NewReader(body))
	rec := httptest.NewRecorder()
	api.HandleImportApply(rec, asUser(req, u))
	return rec
}

func TestImportApplyIgnoresClientSuppliedDiffContent(t *testing.T) {
	// 复审第 3 条：客户端过去能完全控制 Kind/Name/OrgID/ParentExternalID，
	// 从而在本地建出一个 external_id 由攻击者指定的节点，
	// 让真实的目录节点之后永远被判定为「已存在」而不再导入。
	// 现在服务端重新拉取并重算，客户端只能勾选。
	store := newFakeOrgStore()
	src := &fakeSource{nodes: []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}}
	rbac := newFakeRBACStore()
	api := NewOrgAPI(store, src, authz.NewResolver(rbac))
	u := &User{ID: "u1", Status: UserStatusActive}

	// 客户端伪造一条 LDAP 里根本不存在的项
	rec := applyAs(t, api, u, `{"external_ids":["ou=finance"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var res ImportResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Zero(t, res.Created, "LDAP 里不存在的项不得被创建")

	_, err := store.ByExternal(context.Background(), "ldap", "ou=finance")
	require.ErrorIs(t, err, ErrOrgNotFound)
}

func TestImportApplyAppliesOnlySelectedItems(t *testing.T) {
	store := newFakeOrgStore()
	src := &fakeSource{nodes: []ExternalOrgNode{
		{ExternalID: "ou=rd", Name: "研发中心"},
		{ExternalID: "ou=sales", Name: "销售部"},
	}}
	rbac := newFakeRBACStore()
	api := NewOrgAPI(store, src, authz.NewResolver(rbac))
	u := &User{ID: "u1", Status: UserStatusActive}

	rec := applyAs(t, api, u, `{"external_ids":["ou=rd"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var res ImportResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, 1, res.Created)

	_, err := store.ByExternal(context.Background(), "ldap", "ou=rd")
	require.NoError(t, err)
	_, err = store.ByExternal(context.Background(), "ldap", "ou=sales")
	require.ErrorIs(t, err, ErrOrgNotFound, "没勾选的项不得被应用")
}

func TestImportApplyReturnsWhatWasActuallyApplied(t *testing.T) {
	// 预览与应用之间目录可能变化。服务端按最新事实动作，
	// 并把实际执行的差异项回传，让调用方看见发生了什么。
	store := newFakeOrgStore()
	src := &fakeSource{nodes: []ExternalOrgNode{{ExternalID: "ou=rd", Name: "研发中心"}}}
	rbac := newFakeRBACStore()
	api := NewOrgAPI(store, src, authz.NewResolver(rbac))
	u := &User{ID: "u1", Status: UserStatusActive}

	rec := applyAs(t, api, u, `{"external_ids":["ou=rd"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var res struct {
		Created int        `json:"Created"`
		Applied []DiffItem `json:"applied"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Len(t, res.Applied, 1)
	require.Equal(t, "ou=rd", res.Applied[0].ExternalID)
	require.Equal(t, DiffAdded, res.Applied[0].Kind)
}

func TestImportApplySurfacesSourceFailure(t *testing.T) {
	store := newFakeOrgStore()
	src := &fakeSource{err: context.DeadlineExceeded}
	rbac := newFakeRBACStore()
	api := NewOrgAPI(store, src, authz.NewResolver(rbac))
	u := &User{ID: "u1", Status: UserStatusActive}

	rec := applyAs(t, api, u, `{"external_ids":["ou=rd"]}`)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestSetKeyHolderMarksAndUnmarks(t *testing.T) {
	api, store, _ := newOrgAPI(t)
	ctx := context.Background()
	require.NoError(t, store.Create(ctx, &Org{ID: "gw", Name: "网关组"}))

	req := httptest.NewRequest(http.MethodPut, "/api/orgs/gw/key-holder",
		strings.NewReader(`{"is_key_holder":true}`))
	req.SetPathValue("id", "gw")
	rec := httptest.NewRecorder()
	api.HandleSetKeyHolder(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	got, err := store.Get(ctx, "gw")
	require.NoError(t, err)
	require.True(t, got.IsKeyHolder)

	req = httptest.NewRequest(http.MethodPut, "/api/orgs/gw/key-holder",
		strings.NewReader(`{"is_key_holder":false}`))
	req.SetPathValue("id", "gw")
	rec = httptest.NewRecorder()
	api.HandleSetKeyHolder(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	got, err = store.Get(ctx, "gw")
	require.NoError(t, err)
	require.False(t, got.IsKeyHolder)
}

func TestSetKeyHolderUnknownNodeIs404(t *testing.T) {
	api, _, _ := newOrgAPI(t)

	req := httptest.NewRequest(http.MethodPut, "/api/orgs/nope/key-holder",
		strings.NewReader(`{"is_key_holder":true}`))
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	api.HandleSetKeyHolder(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSetKeyHolderRejectsMalformedBody(t *testing.T) {
	api, _, _ := newOrgAPI(t)

	req := httptest.NewRequest(http.MethodPut, "/api/orgs/gw/key-holder",
		strings.NewReader(`{not json`))
	req.SetPathValue("id", "gw")
	rec := httptest.NewRecorder()
	api.HandleSetKeyHolder(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// nudgeCount 数一个 Syncer 被 Nudge 了几次。
func nudgeCount(s *Syncer) int { return len(s.trigger) }

func TestCreateNudges(t *testing.T) {
	api, store, _ := newOrgAPI(t)
	syncer := NewSyncer(SyncerDeps{Orgs: store, Admin: newFakeLiteLLM()})
	api = api.WithNudger(syncer)

	rec := httptest.NewRecorder()
	api.HandleCreate(rec, httptest.NewRequest(http.MethodPost, "/api/orgs",
		strings.NewReader(`{"name":"研发中心"}`)))

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, 1, nudgeCount(syncer), "建节点后应触发同步")
}

func TestRenameNudges(t *testing.T) {
	api, store, _ := newOrgAPI(t)
	syncer := NewSyncer(SyncerDeps{Orgs: store, Admin: newFakeLiteLLM()})
	api = api.WithNudger(syncer)
	require.NoError(t, store.Create(context.Background(), &Org{ID: "rd", Name: "研发中心"}))

	req := httptest.NewRequest(http.MethodPatch, "/api/orgs/rd/name",
		strings.NewReader(`{"name":"技术中心"}`))
	req.SetPathValue("id", "rd")
	rec := httptest.NewRecorder()
	api.HandleRename(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, nudgeCount(syncer), "改名后应触发同步")
}

func TestMoveNudges(t *testing.T) {
	// 移动会改变整棵子树的深度，进而改变哪些节点是 Organization——
	// 但写路径只 Nudge 一下，由全量对账自己算清楚。
	//
	// 复用既有的 visibilityFixture + moveAs：HandleMove 要走真实的目标父节点
	// 权限判定，自己拼 context 与授予很容易拼出一个假的 204。
	api, store, rbac := visibilityFixture(t)
	syncer := NewSyncer(SyncerDeps{Orgs: store, Admin: newFakeLiteLLM()})
	api = api.WithNudger(syncer)

	u := &User{ID: "u1", Status: UserStatusActive}
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g1", UserID: "u1", RoleID: authz.RolePlatformAdmin,
	}))

	rec := moveAs(t, api, u, "gw", strp("sales"))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, nudgeCount(syncer), "移动后应触发同步")
}

func TestFailedWriteDoesNotNudge(t *testing.T) {
	// 写失败时不该触发同步——没有任何变化需要推送。
	api, store, _ := newOrgAPI(t)
	syncer := NewSyncer(SyncerDeps{Orgs: store, Admin: newFakeLiteLLM()})
	api = api.WithNudger(syncer)

	// 改一个不存在的节点。
	req := httptest.NewRequest(http.MethodPatch, "/api/orgs/nope/name",
		strings.NewReader(`{"name":"新名字"}`))
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	api.HandleRename(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Zero(t, nudgeCount(syncer), "写失败不该触发同步")
}

func TestDeleteRejectsNodeWithChildrenSoCascadeIsSafe(t *testing.T) {
	// 删除传播的安全性论证依赖这条守卫：被删节点一定没有子节点，
	// 因此它下面不可能存在别的被标记节点，级联删除波及不到不该删的实体。
	api, store, _ := newOrgAPI(t)
	ctx := context.Background()
	require.NoError(t, store.Create(ctx, &Org{ID: "root", Name: "集团"}))
	rootID := "root"
	require.NoError(t, store.Create(ctx, &Org{ID: "child", Name: "子部门", ParentID: &rootID}))

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/root", nil)
	req.SetPathValue("id", "root")
	rec := httptest.NewRecorder()
	api.HandleDelete(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "org_has_children")
}
