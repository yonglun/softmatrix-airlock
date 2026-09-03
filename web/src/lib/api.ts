/** 后端统一的错误形状：{"error":{"code":"...","message":"..."}} */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

/**
 * 未登录时跳登录，并带上当前路径。
 *
 * 前端不碰会话 cookie——它是 HttpOnly，本来也碰不到。「有没有登录」
 * 这个判断完全由服务端的 401 给出。redirect_to 只能是同源路径，
 * 服务端的 sanitizeRedirect 会把其它一切打回 /。
 */
function gotoLogin(): void {
  window.location.href =
    '/auth/login?redirect_to=' + encodeURIComponent(window.location.pathname)
}

async function handle<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    gotoLogin()
    throw new ApiError(401, 'no_session', '未登录')
  }

  if (res.status === 204) {
    return undefined as T
  }

  let body: unknown
  try {
    body = await res.json()
  } catch {
    if (res.ok) return undefined as T
    throw new ApiError(res.status, 'unexpected_response', `服务端返回了非预期的响应（${res.status}）`)
  }

  if (res.ok) return body as T

  // 直接用后端的 message：那些文案本来就是写给人看的中文，
  // 且随业务逻辑一起维护。前端再存一份只会两边漂移。
  const err = (body as { error?: { code?: string; message?: string } })?.error
  throw new ApiError(
    res.status,
    err?.code ?? 'unknown',
    err?.message ?? `请求失败（${res.status}）`,
  )
}

export async function apiGet<T>(path: string): Promise<T> {
  return handle<T>(await fetch(path, { credentials: 'same-origin' }))
}

export async function apiSend<T>(
  method: 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return handle<T>(res)
}
