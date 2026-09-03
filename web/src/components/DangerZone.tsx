'use client'

import { useState } from 'react'
import { App, Button, Card, Input, Space, Typography } from 'antd'
import { apiSend, ApiError } from '@/lib/api'
import type { RevokeResult } from '@/lib/types'

/** 后端 HandleRevokeAll 要求原样带上的确认串。前端**不代填**。 */
const CONFIRMATION = 'REVOKE ALL KEYS'

/**
 * 全局紧急吊销。break glass，不可逆。
 *
 * 两道闸与后端对齐：全局 key:revoke_all 管住「谁能按」，确认串管住
 * 「不会手滑按到」。确认串必须由用户手打——前端若从常量里自动填上，
 * 就等于把后端刻意加的第二道闸拆了。
 *
 * 隐藏这个区块只是界面便利，不是授权：真正的闸在路由中间件上
 * （Permission: key:revoke_all, Target: 全局）。这里用
 * global_permissions 判定是准确的——key:revoke_all 是 ScopeGlobal
 * 权限，只可能全局持有，不存在会被漏掉的节点级授予。
 */
export function DangerZone({
  canRevokeAll,
  onDone,
}: {
  canRevokeAll: boolean
  onDone: () => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)

  if (!canRevokeAll) return null

  const run = async () => {
    setBusy(true)
    try {
      const res = await apiSend<RevokeResult>('POST', '/api/keys/revoke-all', {
        confirm: CONFIRMATION,
      })
      message.success(`已紧急吊销 ${res.revoked} 把密钥`)
      setTyped('')
      await onDone()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('全局吊销失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card
      title="危险操作"
      style={{ borderColor: '#ffa39e', marginTop: 24 }}
      styles={{ header: { color: '#cf1322' } }}
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <Typography.Text>
          紧急吊销<strong>全系统</strong>密钥。不可逆，全公司所有客户端会立刻失效。 确认请原样输入{' '}
          <Typography.Text code>{CONFIRMATION}</Typography.Text>。
        </Typography.Text>
        <Input
          aria-label="确认串"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder={CONFIRMATION}
        />
        <Button
          danger
          type="primary"
          disabled={typed !== CONFIRMATION}
          loading={busy}
          onClick={run}
        >
          紧急吊销全系统密钥
        </Button>
      </Space>
    </Card>
  )
}
