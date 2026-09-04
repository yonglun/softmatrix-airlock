import { describe, expect, it } from 'vitest'
import { byID, userLabel } from './users'
import type { User } from './types'

function user(over: Partial<User>): User {
  return {
    ID: 'u1',
    ExternalID: 'ext-1',
    Email: 'a@x.com',
    DisplayName: '张三',
    Status: 'active',
    PrimaryOrgID: null,
    ...over,
  }
}

describe('userLabel', () => {
  it('有显示名时给出「显示名（邮箱）」', () => {
    expect(userLabel(user({}), 'u1')).toBe('张三（a@x.com）')
  })

  it('没有显示名时退回邮箱', () => {
    expect(userLabel(user({ DisplayName: '' }), 'u1')).toBe('a@x.com')
  })

  it('查不到这个人时露出原始 ID，而不是显示空白', () => {
    // 授予表里的 user_id 可能指向一个已停用、不在活跃名单里的人。
    // 显示空白会让那一行看起来像坏数据，露出 ID 至少还能追查。
    expect(userLabel(undefined, 'u-ghost')).toBe('u-ghost')
  })
})

describe('byID', () => {
  it('建出 id → user 的查找表', () => {
    const map = byID([user({ ID: 'a' }), user({ ID: 'b' })])
    expect(Object.keys(map).sort()).toEqual(['a', 'b'])
    expect(map.a.Email).toBe('a@x.com')
  })
})
