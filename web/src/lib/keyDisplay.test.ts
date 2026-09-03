import { describe, expect, it } from 'vitest'
import { rotationCell } from './keyDisplay'
import type { ApiKey } from './types'

function key(over: Partial<ApiKey>): ApiKey {
  return {
    id: 'k1',
    key_prefix: 'ak-abc',
    org_id: 'gw',
    user_id: 'u1',
    name: '测试',
    status: 'active',
    models: [],
    max_budget: null,
    budget_duration: null,
    rpm_limit: null,
    tpm_limit: null,
    expires_at: null,
    rotated_at: null,
    prev_key_expires_at: null,
    created_at: '2026-09-03T00:00:00Z',
    ...over,
  }
}

describe('rotationCell', () => {
  it('没轮换过就什么都不说', () => {
    expect(rotationCell(key({}))).toBe('—')
  })

  it('共存窗口还没过时，说清旧凭据还能用到什么时候', () => {
    const future = new Date(Date.now() + 3600_000).toISOString()
    expect(rotationCell(key({ rotated_at: future, prev_key_expires_at: future }))).toContain(
      '旧凭据',
    )
  })

  it('窗口已过就不再说旧凭据可用——那会是危险的错误信息', () => {
    const past = new Date(Date.now() - 3600_000).toISOString()
    const text = rotationCell(key({ rotated_at: past, prev_key_expires_at: past }))
    expect(text).not.toContain('旧凭据')
    expect(text).toContain('已轮换')
  })
})
