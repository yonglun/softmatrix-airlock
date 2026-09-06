import { describe, expect, it } from 'vitest'
import { yuan } from './money'

describe('yuan', () => {
  it('把 Micro 换算成元（1 Micro = 1e-6 元）', () => {
    expect(yuan(1_500_000)).toBe('¥1.5000')
  })

  it('零就是零，不显示成空白', () => {
    expect(yuan(0)).toBe('¥0.0000')
  })

  it('不足一分的金额也要显示出来，而不是舍成 0', () => {
    // 单次调用的成本经常小于一分钱。舍掉的话整张表会全是 ¥0.00，
    // 让人以为没花钱。
    expect(yuan(1234)).toBe('¥0.0012')
  })

  it('大额不丢精度', () => {
    expect(yuan(123_456_789_000)).toBe('¥123456.7890')
  })
})
