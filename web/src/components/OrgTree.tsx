'use client'

import { Tree } from 'antd'
import type { DataNode } from 'antd/es/tree'
import type { Org } from '@/lib/types'

/**
 * 把扁平的组织列表按 ParentID 组装成 antd Tree 要的结构。
 *
 * 单独导出是为了能单测：「可见范围不含根节点」那条回退分支在界面上
 * 很难构造（要一个恰好只授到中层节点的账号），在单测里却是一行的事。
 */
export function toTree(orgs: Org[]): DataNode[] {
  const byParent = new Map<string | null, Org[]>()
  for (const o of orgs) {
    const list = byParent.get(o.ParentID) ?? []
    list.push(o)
    byParent.set(o.ParentID, list)
  }
  const build = (parent: string | null): DataNode[] =>
    (byParent.get(parent) ?? []).map((o) => ({
      key: o.ID,
      title: o.IsKeyHolder ? `${o.Name}（密钥边界）` : o.Name,
      children: build(o.ID),
    }))
  // 可见范围可能不含根节点，此时以列表里最浅的那些作为根。
  const roots = build(null)
  if (roots.length > 0) return roots
  const ids = new Set(orgs.map((o) => o.ID))
  return orgs
    .filter((o) => o.ParentID === null || !ids.has(o.ParentID))
    .map((o) => ({ key: o.ID, title: o.Name, children: build(o.ID) }))
}

/**
 * 组织树。选中项由调用方持有——两个页面对「选中一个节点」的后续动作
 * 完全不同（组织页要增删，密钥页要拉这个节点的密钥）。
 */
export function OrgTree({
  orgs,
  selected,
  onSelect,
}: {
  orgs: Org[]
  selected: string | null
  onSelect: (id: string | null) => void
}) {
  return (
    <Tree
      treeData={toTree(orgs)}
      defaultExpandAll
      selectedKeys={selected ? [selected] : []}
      onSelect={(keys) => onSelect(keys.length > 0 ? String(keys[0]) : null)}
    />
  )
}
