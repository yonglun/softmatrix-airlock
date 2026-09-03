'use client'

import { useEffect } from 'react'
import { useRouter } from 'next/navigation'
import { Spin } from 'antd'
import { useSession } from '@/lib/session'
import { visibleWorkbenches } from '@/components/workbenches'

export default function Home() {
  const { session, loading } = useSession()
  const router = useRouter()

  useEffect(() => {
    if (loading || !session) return
    const first = visibleWorkbenches(session.workbenches)[0]
    // 任何登录用户至少有「我的空间」，因此 first 正常不会为空；
    // 真为空时停在这一页也好过跳到一个自己没权限的地方。
    if (first) router.replace(first.home)
  }, [loading, session, router])

  return <Spin tip="正在进入控制台" fullscreen />
}
