'use client'

import { useState } from 'react'
import { App, Button, Card, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd'
import { apiSend, ApiError } from '@/lib/api'
import { userLabel } from '@/lib/users'
import type { Org, User } from '@/lib/types'

/**
 * 该节点的成员——即 primary_org_id 指向这个节点的用户。
 *
 * 「移出」把 primary_org_id 置空。要如实提醒的是：这条路由用
 * TargetFromBody("org_id") 取判定目标，移进某节点只要该节点的
 * member:assign，而移出（org_id 传 null）目标退化为 nil、判定变成
 * **全局** member:assign。这是既有路由的行为，本期不改，但要让 403
 * 的原因说得通，不能让人对着按钮困惑。
 */
export function OrgMembers({
  org,
  users,
  onChanged,
}: {
  org: Org
  users: User[]
  onChanged: () => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [adding, setAdding] = useState<string | undefined>()

  const members = users.filter((u) => u.PrimaryOrgID === org.ID)
  const outsiders = users.filter((u) => u.PrimaryOrgID !== org.ID)

  const assign = async (userID: string, orgID: string | null, ok: string) => {
    try {
      await apiSend('PUT', `/api/users/${userID}/primary-org`, { org_id: orgID })
      message.success(ok)
      await onChanged()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('操作失败')
    }
  }

  return (
    <Card title="成员" size="small" style={{ marginTop: 16 }}>
      <Space style={{ marginBottom: 12 }}>
        <Select
          showSearch
          style={{ width: 260 }}
          placeholder="选择要加入该节点的用户"
          value={adding}
          onChange={setAdding}
          optionFilterProp="label"
          options={outsiders.map((u) => ({ value: u.ID, label: userLabel(u, u.ID) }))}
        />
        <Button
          type="primary"
          disabled={!adding}
          onClick={async () => {
            if (!adding) return
            await assign(adding, org.ID, '已加入该节点')
            setAdding(undefined)
          }}
        >
          加入
        </Button>
      </Space>

      <Table<User>
        rowKey="ID"
        dataSource={members}
        size="small"
        pagination={false}
        locale={{ emptyText: '该节点下还没有成员' }}
        columns={[
          { title: '用户', render: (_, u) => userLabel(u, u.ID) },
          {
            title: '状态',
            dataIndex: 'Status',
            render: (s: string) => (
              <Tag color={s === 'active' ? 'green' : 'default'}>
                {s === 'active' ? '在职' : s}
              </Tag>
            ),
          },
          {
            title: '操作',
            render: (_, u) => (
              <Popconfirm
                title="确认移出该节点？"
                description="移出后该用户将没有归属节点。这一步需要全局的成员指派权限。"
                onConfirm={() => assign(u.ID, null, '已移出')}
              >
                <Button type="link" danger>
                  移出
                </Button>
              </Popconfirm>
            ),
          },
        ]}
      />
      <Typography.Text type="secondary">
        成员归属只决定「人属于哪个节点」，不等于权限；权限在「角色与权限」页管理。
      </Typography.Text>
    </Card>
  )
}
