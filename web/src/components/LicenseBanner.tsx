'use client'

import { Alert } from 'antd'
import { useEffect, useState } from 'react'
import { apiGet } from '@/lib/api'
import type { LicenseInfo } from '@/lib/types'

/** 临期提醒的提前量。提前预警比事后红条有用得多。 */
const WARN_DAYS = 30

function formatDay(iso: string): string {
  return iso.slice(0, 10)
}

/**
 * 授权状态横幅。
 *
 * 自己取数而不是由父组件传入：它挂在 AppShell 上、每个页面都要，
 * 层层透传只会把一个全局提示变成所有页面的必填 prop。
 *
 * 取数失败时静默不显示——横幅是辅助信息，不该因为一次网络抖动
 * 在所有页面顶部糊一条错误。
 */
export function LicenseBanner() {
  const [info, setInfo] = useState<LicenseInfo | null>(null)

  useEffect(() => {
    let alive = true
    apiGet<LicenseInfo>('/api/license')
      .then((v) => {
        if (alive) setInfo(v)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [])

  if (!info) return null

  const seatsFull = info.seats_used >= info.seats
  const seatHint = seatsFull ? '席位已满，新成员将无法登录；停用离职成员可腾出席位。' : ''

  if (info.status === 'expired') {
    return (
      <Alert
        type="error"
        banner
        message={
          `授权已于 ${info.expires_at ? formatDay(info.expires_at) : '—'} 到期，` +
          '管理操作已暂停；查询与吊销不受影响，AI 调用不受影响。'
        }
      />
    )
  }

  if (info.status === 'trial') {
    return (
      <Alert
        type="warning"
        banner
        message={`未配置授权文件，当前为试用模式（上限 ${info.seats} 席位）。${seatHint}`}
      />
    )
  }

  if (info.days_remaining !== undefined && info.days_remaining <= WARN_DAYS) {
    return (
      <Alert type="warning" banner message={`授权将于 ${info.days_remaining} 天后到期。${seatHint}`} />
    )
  }

  if (seatsFull) {
    return <Alert type="warning" banner message={seatHint} />
  }

  // 正常状态不该占用户的屏幕。
  return null
}
