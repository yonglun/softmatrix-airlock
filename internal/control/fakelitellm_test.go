package control

import (
	"context"
	"errors"
	"sync"

	"github.com/softmatrix/airlock/internal/litellm"
)

// fakeLiteLLM 是 LiteLLMAdmin 的内存实现。
//
// 带互斥锁：Task 9 的 Run 测试会在一个后台 goroutine 里跑对账，
// 同时测试主 goroutine 通过 orgCount()/teamOrgID() 之类的访问器读取结果——
// 不加锁在 -race 下必炸。
type fakeLiteLLM struct {
	mu    sync.Mutex
	orgs  map[string]litellm.Organization
	teams map[string]litellm.Team

	// listOrgsErr / listTeamsErr 模拟拉取现状失败。
	listOrgsErr  error
	listTeamsErr error
	// failCreateOrg / failCreateTeam 指定哪些 ID 创建时报错。
	failCreateOrg  map[string]bool
	failCreateTeam map[string]bool

	// 调用记录，用于断言顺序与幂等。
	calls []string
}

func newFakeLiteLLM() *fakeLiteLLM {
	return &fakeLiteLLM{
		orgs:           map[string]litellm.Organization{},
		teams:          map[string]litellm.Team{},
		failCreateOrg:  map[string]bool{},
		failCreateTeam: map[string]bool{},
	}
}

// setOrgFailure / setTeamFailure 让测试在别的 goroutine 跑对账之前，
// 通过加锁的方式配置失败注入，而不是直接写 map 字段。
func (f *fakeLiteLLM) setOrgFailure(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCreateOrg[id] = true
}

// orgCount / teamOrganizationID / hasOrg / hasTeam / callsSnapshot 是加锁的
// 只读访问器，供测试在另一个 goroutine 运行 Syncer.Run 时安全地检查结果。
func (f *fakeLiteLLM) orgCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.orgs)
}

func (f *fakeLiteLLM) hasOrg(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.orgs[id]
	return ok
}

func (f *fakeLiteLLM) orgAlias(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.orgs[id].Alias
}

func (f *fakeLiteLLM) hasTeam(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.teams[id]
	return ok
}

func (f *fakeLiteLLM) teamAlias(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.teams[id].Alias
}

func (f *fakeLiteLLM) teamOrganizationID(id string) *string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.teams[id].OrganizationID
}

func (f *fakeLiteLLM) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeLiteLLM) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeLiteLLM) setOrg(o litellm.Organization) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgs[o.ID] = o
}

func (f *fakeLiteLLM) setTeam(t litellm.Team) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teams[t.ID] = t
}

func (f *fakeLiteLLM) ListOrganizations(context.Context) ([]litellm.Organization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listOrgsErr != nil {
		return nil, f.listOrgsErr
	}
	out := make([]litellm.Organization, 0, len(f.orgs))
	for _, o := range f.orgs {
		out = append(out, o)
	}
	return out, nil
}

func (f *fakeLiteLLM) CreateOrganization(_ context.Context, o litellm.Organization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create-org:"+o.ID)
	if f.failCreateOrg[o.ID] {
		return errors.New("模拟创建组织失败")
	}
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeLiteLLM) UpdateOrganization(_ context.Context, o litellm.Organization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "update-org:"+o.ID)
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeLiteLLM) DeleteOrganization(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete-org:"+id)
	delete(f.orgs, id)
	// 真实的 LiteLLM 会级联删掉挂在该组织下的全部 Team，fake 必须照做，
	// 否则测试会对删除传播的安全性给出过于乐观的结论。
	for tid, t := range f.teams {
		if t.OrganizationID != nil && *t.OrganizationID == id {
			delete(f.teams, tid)
		}
	}
	return nil
}

func (f *fakeLiteLLM) ListTeams(context.Context) ([]litellm.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listTeamsErr != nil {
		return nil, f.listTeamsErr
	}
	out := make([]litellm.Team, 0, len(f.teams))
	for _, t := range f.teams {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeLiteLLM) CreateTeam(_ context.Context, t litellm.Team) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create-team:"+t.ID)
	if f.failCreateTeam[t.ID] {
		return errors.New("模拟创建团队失败")
	}
	f.teams[t.ID] = t
	return nil
}

func (f *fakeLiteLLM) UpdateTeam(_ context.Context, t litellm.Team) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "update-team:"+t.ID)
	f.teams[t.ID] = t
	return nil
}

func (f *fakeLiteLLM) DeleteTeam(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete-team:"+id)
	delete(f.teams, id)
	return nil
}
