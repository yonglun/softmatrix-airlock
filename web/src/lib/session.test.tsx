import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { SessionProvider, useSession } from './session'

const originalFetch = global.fetch

afterEach(() => {
  global.fetch = originalFetch
  vi.unstubAllGlobals()
})

function mockWhoami(body: unknown, status = 200) {
  global.fetch = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  }) as unknown as typeof fetch
}

function Probe() {
  const { session, loading } = useSession()
  if (loading) return <div>加载中</div>
  return <div>工作台：{session?.workbenches.join(',')}</div>
}

describe('SessionProvider', () => {
  it('加载完成后把 whoami 交给子组件', async () => {
    mockWhoami({
      user: { ID: 'u1', Email: 'a@x.com' },
      grants: [],
      global_permissions: [],
      workbenches: ['my-space', 'platform'],
    })

    render(
      <SessionProvider>
        <Probe />
      </SessionProvider>,
    )

    expect(screen.getByText('加载中')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText('工作台：my-space,platform')).toBeInTheDocument()
    })
  })

  it('未登录时不渲染子组件——由 api 层跳登录，这里只需不闪烁出内容', async () => {
    vi.stubGlobal('location', { pathname: '/', href: '' })
    mockWhoami({ error: { code: 'no_session', message: '未登录' } }, 401)

    render(
      <SessionProvider>
        <Probe />
      </SessionProvider>,
    )

    await waitFor(() => {
      expect(screen.queryByText(/工作台：/)).not.toBeInTheDocument()
    })
  })
})
