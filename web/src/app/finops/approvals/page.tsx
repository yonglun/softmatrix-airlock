'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Empty, Popconfirm, Space, Table } from 'antd'
import { AppShell } from '@/components/AppShell'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import type { ApiRequest } from '@/lib/types'

export default function ApprovalsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [rows, setRows] = useState<ApiRequest[]>([])

  const reload = useCallback(async () => {
    try {
      // 只返回调用者有权审批的那些——可见范围由服务端按
      // key:write 的 Scopes 过滤，前端不做二次判断。
      setRows(await apiGet<ApiRequest[]>('/api/requests/to-approve'))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }, [message])

  useEffect(() => {
    void reload()
  }, [reload])

  const decide = async (id: string, action: 'approve' | 'reject') => {
    try {
      await apiSend('POST', `/api/requests/${id}/${action}`)
      message.success(action === 'approve' ? '已批准' : '已驳回')
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

  if (loading || !session) return null

  return (
    <AppShell workbenches={session.workbenches}>
      <Table<ApiRequest>
        rowKey="id"
        dataSource={rows}
        pagination={false}
        locale={{ emptyText: <Empty description="没有待审批的申请" /> }}
        columns={[
          { title: '类型', dataIndex: 'kind', render: (k: string) => (k === 'quota_bump' ? '临时提额' : '新密钥') },
          { title: '名称', dataIndex: 'key_name', render: (v) => v ?? '—' },
          { title: '节点', dataIndex: 'org_id' },
          { title: '申请人', dataIndex: 'requester_id' },
          { title: '理由', dataIndex: 'reason' },
          {
            title: '操作',
            render: (_, r) => (
              <Space>
                <Popconfirm title="确认批准？" onConfirm={() => decide(r.id, 'approve')}>
                  <Button type="link">批准</Button>
                </Popconfirm>
                <Popconfirm title="确认驳回？" onConfirm={() => decide(r.id, 'reject')}>
                  <Button type="link" danger>
                    驳回
                  </Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
    </AppShell>
  )
}
