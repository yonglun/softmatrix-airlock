package control

import (
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
