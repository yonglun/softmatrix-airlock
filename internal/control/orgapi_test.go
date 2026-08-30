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
)

// fakeOrgStore 是内存组织树，让 API 层可以脱离 Postgres 单测。
type fakeOrgStore struct {
	mu   sync.Mutex
	data map[string]*Org
}

func newFakeOrgStore() *fakeOrgStore {
	return &fakeOrgStore{data: map[string]*Org{}}
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
	return NewOrgAPI(store, src), store, src
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
	api, store, _ := newOrgAPI(t)

	body := `{"items":[{"Kind":"added","ExternalID":"ou=rd","Name":"研发中心"}]}`
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
