import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { apiGet, apiSend, ApiError } from './api'

const originalFetch = global.fetch

function mockFetch(status: number, body: unknown) {
  global.fetch = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  }) as unknown as typeof fetch
}

beforeEach(() => {
  vi.stubGlobal('location', { pathname: '/platform/orgs', href: '' })
})

afterEach(() => {
  global.fetch = originalFetch
  vi.unstubAllGlobals()
})

describe('apiGet', () => {
  it('成功时直接返回解析后的 JSON', async () => {
    mockFetch(200, [{ ID: 'gw', Name: '网关组' }])
    await expect(apiGet('/api/orgs')).resolves.toEqual([{ ID: 'gw', Name: '网关组' }])
  })

  it('把后端的 message 原样带进错误——不在前端另维护一份文案表', async () => {
    mockFetch(409, { error: { code: 'org_has_children', message: '组织节点下还有子节点' } })

    await expect(apiGet('/api/orgs')).rejects.toMatchObject({
      status: 409,
      code: 'org_has_children',
      message: '组织节点下还有子节点',
    })
  })

  it('后端返回非预期结构时给出兜底文案，而不是抛出解析异常', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 502,
      json: async () => {
        throw new Error('not json')
      },
    }) as unknown as typeof fetch

    const err = await apiGet('/api/orgs').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(502)
    expect((err as ApiError).message).toBeTruthy()
  })

  it('401 时跳登录并带上当前路径', async () => {
    mockFetch(401, { error: { code: 'no_session', message: '未登录' } })

    await apiGet('/api/whoami').catch(() => undefined)

    expect(window.location.href).toBe(
      '/auth/login?redirect_to=' + encodeURIComponent('/platform/orgs'),
    )
  })
})

describe('apiSend', () => {
  it('带上 JSON 头与序列化后的 body', async () => {
    mockFetch(201, { id: 'r1' })
    await apiSend('POST', '/api/requests', { kind: 'new_key' })

    const [, init] = (global.fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(init.method).toBe('POST')
    expect(init.headers['Content-Type']).toBe('application/json')
    expect(JSON.parse(init.body)).toEqual({ kind: 'new_key' })
  })

  it('204 无响应体时返回 undefined 而不是抛错', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 204,
      json: async () => {
        throw new Error('no body')
      },
    }) as unknown as typeof fetch

    await expect(apiSend('POST', '/api/requests/r1/approve')).resolves.toBeUndefined()
  })
})
