import { describe, expect, it } from 'vitest'
import { exportURL } from './analytics'

describe('exportURL', () => {
  it('把当前筛选条件原样带进导出链接', () => {
    // 漏掉任何一个条件，导出的就会是与页面不一致的数据——
    // 那比导不出来更糟，因为没人会发现。
    const url = exportURL('/api/usage/summary', {
      from: '2026-09-01',
      to: '2026-09-08',
      group_by: 'model',
    })
    expect(url).toBe(
      '/api/usage/summary?from=2026-09-01&to=2026-09-08&group_by=model&format=csv',
    )
  })

  it('跳过空值，不产生 foo= 这样的空参数', () => {
    const url = exportURL('/api/audit/records', {
      from: '2026-09-01',
      org_id: '',
      user_id: undefined,
    })
    expect(url).toBe('/api/audit/records?from=2026-09-01&format=csv')
  })

  it('对参数值转义，避免模型名里的特殊字符破坏链接', () => {
    // URLSearchParams 按查询字符串的标准约定把空格编码成 +（不是 %20）——
    // 这是合法的 application/x-www-form-urlencoded 形式，Go 的
    // net/url 会正确解出空格，不需要额外处理。
    const url = exportURL('/api/audit/records', { model: 'a/b c' })
    expect(url).toBe('/api/audit/records?model=a%2Fb+c&format=csv')
  })
})
