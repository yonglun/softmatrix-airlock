'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Card, DatePicker, Input, Space, Switch, Table, Tag } from 'antd'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { AppShell } from '@/components/AppShell'
import { useSession } from '@/lib/session'
import { apiGet, ApiError } from '@/lib/api'
import { yuan } from '@/lib/money'
import { exportURL } from '@/lib/analytics'
import { byID, userLabel } from '@/lib/users'
import type { AuditRecord, Org, User } from '@/lib/types'

const PAGE_SIZE = 50

export default function AuditPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [range, setRange] = useState<[Dayjs, Dayjs]>([dayjs().subtract(7, 'day'), dayjs()])
  const [orgID, setOrgID] = useState('')
  const [userID, setUserID] = useState('')
  const [model, setModel] = useState('')
  const [onlyErrors, setOnlyErrors] = useState(false)
  const [page, setPage] = useState(0)
  const [rows, setRows] = useState<AuditRecord[]>([])
  const [orgs, setOrgs] = useState<Org[]>([])
  const [users, setUsers] = useState<User[]>([])

  const params = {
    from: range[0].format('YYYY-MM-DD'),
    to: range[1].format('YYYY-MM-DD'),
    org_id: orgID,
    user_id: userID,
    model,
    only_errors: onlyErrors ? 'true' : '',
  }

  useEffect(() => {
    apiGet<Org[]>('/api/orgs')
      .then(setOrgs)
      .catch(() => setOrgs([]))
    apiGet<User[]>('/api/users')
      .then(setUsers)
      .catch(() => setUsers([]))
  }, [])

  const reload = useCallback(async () => {
    try {
      const qs = new URLSearchParams({
        ...params,
        limit: String(PAGE_SIZE),
        offset: String(page * PAGE_SIZE),
      })
      setRows(await apiGet<AuditRecord[]>(`/api/audit/records?${qs.toString()}`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setRows([])
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, orgID, userID, model, onlyErrors, page, message])

  useEffect(() => {
    void reload()
  }, [reload])

  if (loading || !session) return null

  const userMap = byID(users)
  const orgName = (id: string) => orgs.find((o) => o.ID === id)?.Name ?? id

  return (
    <AppShell workbenches={session.workbenches}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Card size="small">
          <Space wrap>
            <DatePicker.RangePicker
              value={range}
              onChange={(v) => {
                if (v && v[0] && v[1]) {
                  setRange([v[0], v[1]])
                  setPage(0)
                }
              }}
              allowClear={false}
            />
            <Input placeholder="组织 ID" value={orgID}
              onChange={(e) => { setOrgID(e.target.value); setPage(0) }} style={{ width: 160 }} />
            <Input placeholder="成员 ID" value={userID}
              onChange={(e) => { setUserID(e.target.value); setPage(0) }} style={{ width: 160 }} />
            <Input placeholder="模型" value={model}
              onChange={(e) => { setModel(e.target.value); setPage(0) }} style={{ width: 140 }} />
            <span>
              只看失败{' '}
              <Switch checked={onlyErrors}
                onChange={(v) => { setOnlyErrors(v); setPage(0) }} />
            </span>
            <Button href={exportURL('/api/audit/records', params)}>导出 CSV</Button>
          </Space>
        </Card>

        <Table<AuditRecord>
          rowKey="request_id"
          dataSource={rows}
          pagination={false}
          size="small"
          locale={{ emptyText: '没有符合条件的调用记录' }}
          columns={[
            { title: '时间', dataIndex: 'ts',
              render: (t: string) => new Date(t).toLocaleString('zh-CN') },
            { title: '组织', dataIndex: 'org_id', render: (id: string) => orgName(id) },
            { title: '成员', dataIndex: 'user_id',
              render: (id: string) => userLabel(userMap[id], id) },
            { title: '模型', dataIndex: 'model' },
            { title: '状态', dataIndex: 'status_code',
              render: (s: number) => (
                <Tag color={s >= 400 ? 'red' : 'green'}>{s}</Tag>
              ) },
            { title: '耗时(ms)', dataIndex: 'latency_ms' },
            { title: '输入 token', dataIndex: 'input_tokens' },
            { title: '输出 token', dataIndex: 'output_tokens' },
            { title: '成本', dataIndex: 'cost_micro', render: (v: number) => yuan(v) },
            { title: '错误', dataIndex: 'error_type', render: (v: string) => v || '—' },
          ]}
        />

        <Space>
          <Button disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>
            上一页
          </Button>
          <span>第 {page + 1} 页</span>
          <Button disabled={rows.length < PAGE_SIZE} onClick={() => setPage((p) => p + 1)}>
            下一页
          </Button>
        </Space>
      </Space>
    </AppShell>
  )
}
