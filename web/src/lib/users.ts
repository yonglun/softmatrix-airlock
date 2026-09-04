import type { User } from './types'

/**
 * 把用户显示成人话。
 *
 * 查不到人时露出原始 ID 而不是空白：授予表里的 user_id 可能指向一个
 * 已停用、不在活跃名单里的人，显示空白会让那一行看起来像坏数据。
 */
export function userLabel(u: User | undefined, fallbackID: string): string {
  if (!u) return fallbackID
  if (u.DisplayName) return `${u.DisplayName}（${u.Email}）`
  return u.Email || fallbackID
}

/** 建 id → user 的查找表。 */
export function byID(users: User[]): Record<string, User> {
  return Object.fromEntries(users.map((u) => [u.ID, u]))
}
