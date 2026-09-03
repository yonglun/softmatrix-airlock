'use client'

import { useState } from 'react'
import { App, InputNumber, Modal, Typography } from 'antd'
import { apiSend, ApiError } from '@/lib/api'
import type { ApiKey, IssuedKey } from '@/lib/types'

/** 与后端 maxRotationWindow 一致（30 天）。 */
const MAX_WINDOW_HOURS = 30 * 24

/**
 * 轮换弹窗。平台侧（替人处置）与我的密钥（自助）共用。
 *
 * 文案里要说清共存窗口不是止血手段：窗口内旧凭据仍然有效，
 * 泄露了该走吊销。后端的 API 也表达不了零窗口，所以这不是可以
 * 靠「把窗口填 0」绕过去的事。
 */
export function KeyRotateModal({
  target,
  onClose,
  onRotated,
}: {
  target: ApiKey | null
  onClose: () => void
  onRotated: (plaintext: string) => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [hours, setHours] = useState(24)
  const [busy, setBusy] = useState(false)

  const run = async () => {
    if (!target) return
    setBusy(true)
    try {
      const rotated = await apiSend<IssuedKey>('POST', `/api/keys/${target.id}/rotate`, {
        window_seconds: hours * 3600,
      })
      await onRotated(rotated.key)
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('轮换失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={target ? `轮换密钥：${target.name}` : '轮换密钥'}
      open={target !== null}
      onCancel={onClose}
      onOk={run}
      confirmLoading={busy}
    >
      <Typography.Paragraph>
        轮换会换发新的客户端凭据。旧凭据在共存窗口内仍然可用，供你替换配置；窗口一过立刻失效。
      </Typography.Paragraph>
      <Typography.Paragraph type="warning">
        如果这把密钥已经泄露，轮换救不了场——窗口内旧凭据仍然有效。那种情况该用吊销。
      </Typography.Paragraph>
      共存窗口（小时）：
      <InputNumber
        min={1}
        max={MAX_WINDOW_HOURS}
        value={hours}
        onChange={(v) => setHours(v ?? 24)}
        style={{ marginLeft: 8 }}
      />
    </Modal>
  )
}
