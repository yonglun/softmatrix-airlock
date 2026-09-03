'use client'

import { Alert, Button, Modal, Typography } from 'antd'

/**
 * 一次性明文弹窗。
 *
 * 签发、轮换、领取都只在响应里给一次明文，关掉就再也拿不到。
 * 这句话必须写在用户眼前，而不是只写在设计文档里。
 */
export function PlaintextKeyModal({
  plaintext,
  title,
  onClose,
}: {
  plaintext: string | null
  title: string
  onClose: () => void
}) {
  return (
    <Modal
      title={title}
      open={plaintext !== null}
      onCancel={onClose}
      footer={<Button onClick={onClose}>我已保存</Button>}
    >
      <Alert
        type="warning"
        showIcon
        message="这串明文只显示这一次"
        description="关闭后无法再次查看。请立刻保存到你的密钥管理器或 CI 变量里。"
        style={{ marginBottom: 12 }}
      />
      <Typography.Paragraph copyable code>
        {plaintext}
      </Typography.Paragraph>
    </Modal>
  )
}
