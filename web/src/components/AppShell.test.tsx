import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { visibleWorkbenches } from './workbenches'

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
    expect(visibleWorkbenches(['my-space', 'security']).map((w) => w.id)).toEqual(['my-space'])
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
})
