package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ImportResult 汇总一次导入实际做了什么。
type ImportResult struct {
	Created      int
	Renamed      int
	MarkedOrphan int
	Skipped      int
}

// ApplyImport 应用用户确认过的差异。
//
// 关键约束：DiffMissing 永远不会删除节点，只记一笔孤儿标记。
// 这是刻意用「需要人工清理」换「不可能误删计费结构」。
func ApplyImport(ctx context.Context, store OrgStore, source string, items []DiffItem) (ImportResult, error) {
	var res ImportResult

	// 新增项按树的层级排序：父节点必须先建，否则子节点找不到 parent。
	added := sortAddedByDepth(items)

	// external_id -> Airlock 节点 ID，供后续子节点解析父节点用
	extToID := make(map[string]string, len(added))

	for _, it := range added {
		if existing, err := store.ByExternal(ctx, source, it.ExternalID); err == nil {
			extToID[it.ExternalID] = existing.ID
			res.Skipped++
			continue
		} else if !errors.Is(err, ErrOrgNotFound) {
			return res, fmt.Errorf("查询已有节点失败（external_id=%s）: %w", it.ExternalID, err)
		}

		var parentID *string
		if it.ParentExternalID != "" {
			pid, ok := extToID[it.ParentExternalID]
			if !ok {
				parent, err := store.ByExternal(ctx, source, it.ParentExternalID)
				if err != nil {
					return res, fmt.Errorf(
						"新增节点 %s 的父节点 %s 不存在，导入中止",
						it.ExternalID, it.ParentExternalID)
				}
				pid = parent.ID
			}
			parentID = &pid
		}

		o := &Org{
			ID:             uuid.NewString(),
			ParentID:       parentID,
			Name:           it.Name,
			ExternalSource: &source,
			ExternalID:     &it.ExternalID,
		}
		if err := store.Create(ctx, o); err != nil {
			return res, fmt.Errorf("创建节点失败（external_id=%s）: %w", it.ExternalID, err)
		}
		extToID[it.ExternalID] = o.ID
		res.Created++
	}

	for _, it := range items {
		switch it.Kind {
		case DiffRenamed:
			if err := store.Rename(ctx, it.OrgID, it.Name); err != nil {
				return res, fmt.Errorf("重命名节点失败（org_id=%s）: %w", it.OrgID, err)
			}
			res.Renamed++
		case DiffMissing:
			// 只记录，不删除。孤儿节点由管理员在控制台自行判断处理。
			slog.Warn("IdP 侧已不存在该组织节点，标记为孤儿但不删除",
				"source", source, "external_id", it.ExternalID,
				"org_id", it.OrgID, "name", it.CurrentName)
			res.MarkedOrphan++
		}
	}

	return res, nil
}

// sortAddedByDepth 把新增项按「父节点在前」的顺序排好。
// IdP 返回的顺序不可假定，子节点可能排在父节点前面。
func sortAddedByDepth(items []DiffItem) []DiffItem {
	var added []DiffItem
	for _, it := range items {
		if it.Kind == DiffAdded {
			added = append(added, it)
		}
	}

	inBatch := make(map[string]bool, len(added))
	for _, it := range added {
		inBatch[it.ExternalID] = true
	}

	// 反复扫描：每轮把「父节点不在本批、或父节点已经排好」的项取出来。
	// 组织树深度有限，这个朴素做法足够，且天然容忍批内的环（剩下的原样附加）。
	placed := make(map[string]bool, len(added))
	var out []DiffItem
	for len(out) < len(added) {
		progressed := false
		for _, it := range added {
			if placed[it.ExternalID] {
				continue
			}
			parentPending := it.ParentExternalID != "" &&
				inBatch[it.ParentExternalID] && !placed[it.ParentExternalID]
			if parentPending {
				continue
			}
			out = append(out, it)
			placed[it.ExternalID] = true
			progressed = true
		}
		if !progressed {
			// 批内存在环或悬空父节点，把剩下的原样附加，交给 Create 报错
			for _, it := range added {
				if !placed[it.ExternalID] {
					out = append(out, it)
					placed[it.ExternalID] = true
				}
			}
		}
	}
	return out
}
