package control

import (
	"context"
	"errors"

	"github.com/softmatrix/airlock/internal/litellm"
)

// fakeLiteLLM 是 LiteLLMAdmin 的内存实现。
type fakeLiteLLM struct {
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

func (f *fakeLiteLLM) ListOrganizations(context.Context) ([]litellm.Organization, error) {
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
	f.calls = append(f.calls, "create-org:"+o.ID)
	if f.failCreateOrg[o.ID] {
		return errors.New("模拟创建组织失败")
	}
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeLiteLLM) UpdateOrganization(_ context.Context, o litellm.Organization) error {
	f.calls = append(f.calls, "update-org:"+o.ID)
	f.orgs[o.ID] = o
	return nil
}

func (f *fakeLiteLLM) DeleteOrganization(_ context.Context, id string) error {
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
	f.calls = append(f.calls, "create-team:"+t.ID)
	if f.failCreateTeam[t.ID] {
		return errors.New("模拟创建团队失败")
	}
	f.teams[t.ID] = t
	return nil
}

func (f *fakeLiteLLM) UpdateTeam(_ context.Context, t litellm.Team) error {
	f.calls = append(f.calls, "update-team:"+t.ID)
	f.teams[t.ID] = t
	return nil
}

func (f *fakeLiteLLM) DeleteTeam(_ context.Context, id string) error {
	f.calls = append(f.calls, "delete-team:"+id)
	delete(f.teams, id)
	return nil
}
