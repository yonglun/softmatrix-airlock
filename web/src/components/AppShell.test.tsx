import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { visibleWorkbenches } from './workbenches'
import * as api from '@/lib/api'

const push = vi.fn()
vi.mock('next/navigation', () => ({
  usePathname: () => '/platform/orgs',
  useRouter: () => ({ push }),
}))

describe('visibleWorkbenches', () => {
  it('只挑出服务端给的那些，顺序以服务端为准', () => {
    const got = visibleWorkbenches(['finops', 'my-space'])
    expect(got.map((w) => w.id)).toEqual(['finops', 'my-space'])
  })

  it('忽略前端不认识的 id，而不是崩掉——服务端加了新工作台但前端还没跟上时不该白屏', () => {
    // security 在 P1.5a 前是「前端还不认识」的现成例子；它现在是真实
    // 工作台了，因此换一个仍然不存在的 id 保住这条测试原本要钉住的性质。
    expect(visibleWorkbenches(['my-space', 'not-yet-implemented']).map((w) => w.id))
      .toEqual(['my-space'])
  })
})

describe('AppShell', () => {
  it('只渲染服务端允许的工作台', async () => {
    const { AppShell } = await import('./AppShell')
    render(
      <AppShell workbenches={['my-space', 'platform']}>
        <div>页面内容</div>
      </AppShell>,
    )

    expect(screen.getByText('我的空间')).toBeInTheDocument()
    expect(screen.getByText('平台管理')).toBeInTheDocument()
    expect(screen.queryByText('成本财务')).not.toBeInTheDocument()
    expect(screen.getByText('页面内容')).toBeInTheDocument()
  })

  it('渲染授权横幅', async () => {
    vi.spyOn(api, 'apiGet').mockResolvedValue({
      status: 'trial',
      seats: 5,
      seats_used: 1,
    } as never)

    const { AppShell } = await import('./AppShell')
    render(<AppShell workbenches={['platform']}>内容</AppShell>)

    expect(await screen.findByText(/试用模式/)).toBeInTheDocument()
  })
})
