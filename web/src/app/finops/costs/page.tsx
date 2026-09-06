'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Card, DatePicker, Segmented, Space, Table } from 'antd'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { AppShell } from '@/components/AppShell'
import { useSession } from '@/lib/session'
import { apiGet, ApiError } from '@/lib/api'
import { yuan } from '@/lib/money'
import { exportURL } from '@/lib/analytics'
import { byID, userLabel } from '@/lib/users'
import type { Org, UsageSummaryRow, User } from '@/lib/types'

const DIMENSIONS = [
  { label: '按组织', value: 'org' },
  { label: '按成员', value: 'user' },
  { label: '按模型', value: 'model' },
  { label: '按天', value: 'day' },
]

export default function CostsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [range, setRange] = useState<[Dayjs, Dayjs]>([dayjs().subtract(7, 'day'), dayjs()])
  const [groupBy, setGroupBy] = useState('org')
  const [rows, setRows] = useState<UsageSummaryRow[]>([])
  const [orgs, setOrgs] = useState<Org[]>([])
  const [users, setUsers] = useState<User[]>([])

  const params = {
    from: range[0].format('YYYY-MM-DD'),
    to: range[1].format('YYYY-MM-DD'),
    group_by: groupBy,
  }

  useEffect(() => {
    // 维度值要还原成人话。取不到就退回显示原始 ID——与 P1.4b/c 同样的降级。
    apiGet<Org[]>('/api/orgs')
      .then(setOrgs)
      .catch(() => setOrgs([]))
    apiGet<User[]>('/api/users')
      .then(setUsers)
      .catch(() => setUsers([]))
  }, [])

  const reload = useCallback(async () => {
    try {
      const qs = new URLSearchParams(params).toString()
      setRows(await apiGet<UsageSummaryRow[]>(`/api/usage/summary?${qs}`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setRows([])
    }
    // params 由 range/groupBy 派生，这两个才是真正的依赖
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, groupBy, message])

  useEffect(() => {
    void reload()
  }, [reload])

  if (loading || !session) return null

  const userMap = byID(users)
  const label = (key: string) => {
    if (groupBy === 'org') return orgs.find((o) => o.ID === key)?.Name ?? key
    if (groupBy === 'user') return userLabel(userMap[key], key)
    return key
  }

  return (
    <AppShell workbenches={session.workbenches}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Card size="small">
          <Space wrap>
            <DatePicker.RangePicker
              value={range}
              onChange={(v) => {
                if (v && v[0] && v[1]) setRange([v[0], v[1]])
              }}
              allowClear={false}
            />
            <Segmented options={DIMENSIONS} value={groupBy} onChange={(v) => setGroupBy(String(v))} />
            <Button href={exportURL('/api/usage/summary', params)}>导出 CSV</Button>
          </Space>
        </Card>

        <Table<UsageSummaryRow>
          rowKey="key"
          dataSource={rows}
          pagination={false}
          locale={{ emptyText: '这段时间没有用量记录' }}
          columns={[
            { title: DIMENSIONS.find((d) => d.value === groupBy)?.label ?? '维度',
              dataIndex: 'key', render: (k: string) => label(k) },
            { title: '请求数', dataIndex: 'requests' },
            { title: '输入 token', dataIndex: 'input_tokens' },
            { title: '输出 token', dataIndex: 'output_tokens' },
            { title: '成本', dataIndex: 'cost_micro', render: (v: number) => yuan(v) },
          ]}
        />
      </Space>
    </AppShell>
  )
}
