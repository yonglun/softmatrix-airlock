package control

import (
	"context"
	"sort"
)

// ExternalOrgNode 是从 IdP 通讯录读到的一个组织节点。
type ExternalOrgNode struct {
	ExternalID       string
	ParentExternalID string // "" 表示根节点
	Name             string
}

// DirectorySource 是通讯录来源。LDAP、钉钉、企微各实现一个。
type DirectorySource interface {
	Name() string
	FetchOrgTree(ctx context.Context) ([]ExternalOrgNode, error)
}

type DiffKind string

const (
	DiffAdded   DiffKind = "added"
	DiffRenamed DiffKind = "renamed"
	DiffMissing DiffKind = "missing"
)

// DiffItem 是一条待确认的差异。
type DiffItem struct {
	Kind             DiffKind
	ExternalID       string
	ParentExternalID string
	Name             string // added/renamed：IdP 侧的名字
	CurrentName      string // renamed/missing：Airlock 侧现名
	OrgID            string // renamed/missing：Airlock 侧节点 ID
	DefaultSelected  bool
}

// ComputeDiff 比较 IdP 侧的组织树与 Airlock 侧同来源的节点，产出待确认的差异。
//
// 三条默认行为（设计文档 §5.2）：
//   - 新增：默认勾选。纯增量，安全。
//   - 改名：默认不勾选。Airlock 侧可能是故意改的名，不该被 HR 系统覆盖。
//   - 消失：默认不勾选，且应用时也只标记不删除。「消失即删除」是这类
//     同步功能最经典的事故源——HR 一次组织调整能删掉半个计费结构。
//
// 只比较 external_source 等于 source 的本地节点：手工建的节点（source 为 NULL）
// 和其他 IdP 来源的节点完全不受影响。
func ComputeDiff(remote []ExternalOrgNode, local []*Org, source string) []DiffItem {
	localByExt := make(map[string]*Org, len(local))
	for _, o := range local {
		if o.ExternalSource == nil || *o.ExternalSource != source || o.ExternalID == nil {
			continue
		}
		localByExt[*o.ExternalID] = o
	}

	seen := make(map[string]bool, len(remote))
	var items []DiffItem

	for _, rn := range remote {
		seen[rn.ExternalID] = true

		existing, ok := localByExt[rn.ExternalID]
		if !ok {
			items = append(items, DiffItem{
				Kind:             DiffAdded,
				ExternalID:       rn.ExternalID,
				ParentExternalID: rn.ParentExternalID,
				Name:             rn.Name,
				DefaultSelected:  true,
			})
			continue
		}
		if existing.Name != rn.Name {
			items = append(items, DiffItem{
				Kind:             DiffRenamed,
				ExternalID:       rn.ExternalID,
				ParentExternalID: rn.ParentExternalID,
				Name:             rn.Name,
				CurrentName:      existing.Name,
				OrgID:            existing.ID,
				DefaultSelected:  false,
			})
		}
	}

	for extID, o := range localByExt {
		if seen[extID] {
			continue
		}
		items = append(items, DiffItem{
			Kind:            DiffMissing,
			ExternalID:      extID,
			CurrentName:     o.Name,
			OrgID:           o.ID,
			DefaultSelected: false,
		})
	}

	// 固定排序：先按类型再按 external_id，保证预览页面稳定。
	// 遍历 map 的顺序是随机的，不排序会让同样的输入每次产出不同顺序。
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ExternalID < items[j].ExternalID
	})
	return items
}
