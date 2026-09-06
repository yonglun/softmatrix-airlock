import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { LicenseBanner } from './LicenseBanner'
import type { LicenseInfo } from '@/lib/types'
import * as api from '@/lib/api'

function mockLicense(info: LicenseInfo) {
  vi.spyOn(api, 'apiGet').mockResolvedValue(info as never)
}

describe('LicenseBanner', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('在期且不临期时什么都不显示', async () => {
    mockLicense({ status: 'valid', customer: 'x', seats: 200, seats_used: 10, days_remaining: 300 })
    const { container } = render(<LicenseBanner />)

    await waitFor(() => expect(api.apiGet).toHaveBeenCalledWith('/api/license'))
    expect(container.textContent).toBe('')
  })

  it('过期时显示红条，并说明 AI 调用不受影响', async () => {
    mockLicense({
      status: 'expired',
      customer: 'x',
      expires_at: '2026-09-03T00:00:00Z',
      seats: 200,
      seats_used: 187,
      days_remaining: -3,
    })
    render(<LicenseBanner />)

    const text = await screen.findByText(/授权已于/)
    expect(text.textContent).toContain('管理操作已暂停')
    // 不写这句的话，运维第一反应是拔电源。
    expect(text.textContent).toContain('AI 调用不受影响')
  })

  it('试用模式显示席位上限', async () => {
    mockLicense({ status: 'trial', seats: 5, seats_used: 2 })
    render(<LicenseBanner />)

    expect(await screen.findByText(/试用模式/)).toBeTruthy()
    expect(screen.getByText(/上限 5 席位/)).toBeTruthy()
  })

  it('临期 30 天内显示黄条', async () => {
    mockLicense({
      status: 'valid',
      customer: 'x',
      expires_at: '2026-10-01T00:00:00Z',
      seats: 200,
      seats_used: 10,
      days_remaining: 25,
    })
    render(<LicenseBanner />)

    expect(await screen.findByText(/25 天后到期/)).toBeTruthy()
  })

  it('席位满时把自助解法写在提示里', async () => {
    mockLicense({
      status: 'valid',
      customer: 'x',
      expires_at: '2027-10-01T00:00:00Z',
      seats: 200,
      seats_used: 200,
      days_remaining: 300,
    })
    render(<LicenseBanner />)

    const text = await screen.findByText(/席位已满/)
    expect(text.textContent).toContain('停用离职成员')
  })
})
