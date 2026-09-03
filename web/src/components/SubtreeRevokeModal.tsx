'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Modal, Table, Tag, Typography } from 'antd'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import { KEY_STATUS } from '@/lib/keyDisplay'
import type { ApiKey, Org, RevokeResult } from '@/lib/types'

/** 会被子树吊销打到的状态。与后端 RevokeByOrgSubtree 的 WHERE 条件对齐。 */
const WILL_REVOKE = ['active', 'pending']

/**
 * 子树批量吊销的预览与确认。
 *
 * 打开时用 ?subtree=true 拉列表——服务端那条查询与吊销用的是逐字相同的
 * 节点选择子句，所以这里列出的就是即将被打到的那一批。
 *
 * 预览是快照，不是锁：确认之间行仍可能变（别的管理员刚签发了一把、
 * 审批 worker 刚落一把）。因此成功提示用响应里的实际 revoked 条数，
 * 而不是回显这里算出的 N。
 */
export function SubtreeRevokeModal({
  org,
  onClose,
  onDone,
}: {
  org: Org | null
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [rows, setRows] = useState<ApiKey[]>([])
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!org) return
    try {
      setRows(await apiGet<ApiKey[]>(`/api/orgs/${org.ID}/keys?subtree=true`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setRows([])
    }
  }, [org, message])

  useEffect(() => {
    void load()
  }, [load])

  const willRevoke = rows.filter((k) => WILL_REVOKE.includes(k.status))

  const run = async () => {
    if (!org) return
    setBusy(true)
    try {
      const res = await apiSend<RevokeResult>('POST', `/api/orgs/${org.ID}/keys/revoke`)
      message.success(`已吊销 ${res.revoked} 把密钥`)
      await onDone()
      onClose()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('批量吊销失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={org ? `吊销「${org.Name}」子树下的全部密钥` : '批量吊销'}
      open={org !== null}
      onCancel={onClose}
      onOk={run}
      okText="确认吊销"
      okButtonProps={{ danger: true, disabled: willRevoke.length === 0 }}
      confirmLoading={busy}
      width={720}
    >
      <Typography.Paragraph>
        将吊销其中 <strong>{willRevoke.length}</strong> 把（可用与签发中的）。不可逆，
        使用它们的客户端会立刻失效。
      </Typography.Paragraph>
      <Typography.Paragraph type="secondary">
        下面是这一刻的快照。确认之间若有新密钥签发，实际条数可能不同——以吊销后的提示为准。
      </Typography.Paragraph>
      <Table<ApiKey>
        rowKey="id"
        dataSource={rows}
        size="small"
        pagination={false}
        scroll={{ y: 280 }}
        locale={{ emptyText: '该子树下没有密钥' }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'key_prefix' },
          { title: '所属节点', dataIndex: 'org_id' },
          {
            title: '状态',
            dataIndex: 'status',
            render: (s: string) => {
              const meta = KEY_STATUS[s] ?? { text: s, color: 'default' }
              return <Tag color={meta.color}>{meta.text}</Tag>
            },
          },
        ]}
      />
    </Modal>
  )
}
