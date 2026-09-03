'use client'

import { createContext, useContext, useEffect, useState } from 'react'
import { apiGet } from './api'
import type { Whoami } from './types'

type SessionState = {
  session: Whoami | null
  loading: boolean
}

const SessionContext = createContext<SessionState>({ session: null, loading: true })

export function useSession(): SessionState {
  return useContext(SessionContext)
}

/**
 * 启动时探测一次 whoami。
 *
 * 401 由 api 层负责跳登录，这里只保证在拿到会话之前不把子组件渲染出来——
 * 否则会先闪一下空的工作台再跳走。
 */
export function SessionProvider({ children }: { children: React.ReactNode }) {
  const [state, setState] = useState<SessionState>({ session: null, loading: true })

  useEffect(() => {
    let alive = true
    apiGet<Whoami>('/api/whoami')
      .then((s) => {
        if (alive) setState({ session: s, loading: false })
      })
      .catch(() => {
        // 401 已经在跳登录；其它错误也没有可渲染的会话，
        // 保持 loading 让页面停在占位态而不是渲染半个界面。
        if (alive) setState({ session: null, loading: false })
      })
    return () => {
      alive = false
    }
  }, [])

  // 始终渲染 children，由各页面按 loading / session 自行决定显示什么。
  // 不在这里拦着不渲染：那样连「加载中」都显示不出来。未登录时
  // api 层已经在跳登录，短暂空白可接受。
  return <SessionContext.Provider value={state}>{children}</SessionContext.Provider>
}
