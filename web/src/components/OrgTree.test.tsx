import { describe, expect, it } from 'vitest'
import { toTree } from './OrgTree'
import type { Org } from '@/lib/types'

function org(id: string, parent: string | null, name = id, keyHolder = false): Org {
  return {
    ID: id,
    ParentID: parent,
    Name: name,
    Path: parent === null ? `/${id}` : `/${parent}/${id}`,
    ExternalSource: null,
    ExternalID: null,
    IsKeyHolder: keyHolder,
  }
}

describe('toTree', () => {
  it('按 ParentID 组装层级', () => {
    const tree = toTree([org('root', null), org('child', 'root')])
    expect(tree).toHaveLength(1)
    expect(tree[0].key).toBe('root')
    expect(tree[0].children?.[0].key).toBe('child')
  })

  it('给密钥边界节点加标注', () => {
    const tree = toTree([org('gw', null, '网关组', true)])
    expect(tree[0].title).toBe('网关组（密钥边界）')
  })

  it('可见范围不含根节点时，以最浅的那些作为根——否则整棵树会渲染成空', () => {
    // 只能看到 /root/rd 及其下级、看不到 /root 本身：
    // rd 的 ParentID 指向一个不在列表里的 ID。
    const tree = toTree([org('rd', 'root'), org('gw', 'rd')])
    expect(tree).toHaveLength(1)
    expect(tree[0].key).toBe('rd')
    expect(tree[0].children?.[0].key).toBe('gw')
  })
})
