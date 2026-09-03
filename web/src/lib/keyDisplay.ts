import type { ApiKey } from './types'

/** 密钥状态的中文标签与配色。 */
export const KEY_STATUS: Record<string, { text: string; color: string }> = {
  active: { text: '可用', color: 'green' },
  pending: { text: '签发中', color: 'gold' },
  revoked: { text: '已吊销', color: 'red' },
}

/**
 * 轮换状态的一句话。
 *
 * 窗口没过就报旧凭据的到期时间——那是轮换后唯一需要被看见的信息。
 * 窗口已过就绝不能再说「旧凭据可用」：那会让人以为还有替换时间。
 */
export function rotationCell(k: ApiKey): string {
  if (k.prev_key_expires_at && new Date(k.prev_key_expires_at) > new Date()) {
    return `旧凭据 ${new Date(k.prev_key_expires_at).toLocaleString('zh-CN')} 前仍可用`
  }
  if (k.rotated_at) {
    return `已轮换于 ${new Date(k.rotated_at).toLocaleString('zh-CN')}`
  }
  return '—'
}
