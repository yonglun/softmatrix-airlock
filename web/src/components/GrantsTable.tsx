'use client'

import { Button, Popconfirm, Table, Tag, Typography } from 'antd'
import { byID, userLabel } from '@/lib/users'
import type { EffectiveGrant, Org, Role, User } from '@/lib/types'

const SOURCE_TAG: Record<string, { text: string; color: string }> = {
  direct: { text: '直授', color: 'blue' },
  inherited: { text: '继承', color: 'gold' },
  global: { text: '全局', color: 'purple' },
}

/**
 * 有效权限表：谁对这个节点有权。
 *
 * 只有 source === 'direct' 的行能就地撤销。继承与全局的行只给一句说明——
 * 在这一页撤销一条继承来的授予，实际改动的是**另一个节点**上的行，会同时
 * 波及那个节点下所有其它子树。把它做成就手可点的按钮，等于邀请人误伤。
 */
export function GrantsTable({
  grants,
  users,
  roles,
  orgs,
  onRevoke,
}: {
  grants: EffectiveGrant[]
  users: User[]
  roles: Role[]
  orgs: Org[]
  onRevoke: (id: string) => void | Promise<void>
}) {
  const userMap = byID(users)
  const roleName = (id: string) => roles.find((r) => r.ID === id)?.Name ?? id
  const orgName = (id: string | null) =>
    id === null ? null : (orgs.find((o) => o.ID === id)?.Name ?? id)

  return (
    <Table<EffectiveGrant>
      rowKey="id"
      dataSource={grants}
      pagination={false}
      locale={{ emptyText: '该节点上没有任何人有权' }}
      columns={[
        {
          title: '用户',
          dataIndex: 'user_id',
          render: (id: string) => userLabel(userMap[id], id),
        },
        { title: '角色', dataIndex: 'role_id', render: (id: string) => roleName(id) },
        {
          title: '来源',
          dataIndex: 'source',
          render: (s: string) => {
            const meta = SOURCE_TAG[s] ?? { text: s, color: 'default' }
            return <Tag color={meta.color}>{meta.text}</Tag>
          },
        },
        {
          title: '授予时间',
          dataIndex: 'created_at',
          render: (t: string) => new Date(t).toLocaleString('zh-CN'),
        },
        {
          title: '操作',
          render: (_, g) => {
            if (g.source === 'direct') {
              return (
                <Popconfirm
                  title="确认撤销该授予？"
                  description="撤销后该用户将失去这个节点上的这个角色。"
                  onConfirm={() => onRevoke(g.id)}
                >
                  <Button type="link" danger>
                    撤销
                  </Button>
                </Popconfirm>
              )
            }
            if (g.source === 'global') {
              return <Typography.Text type="secondary">全局授予，需在全局撤销</Typography.Text>
            }
            return (
              <Typography.Text type="secondary">
                需在「{orgName(g.source_org_id)}」上撤销
              </Typography.Text>
            )
          },
        },
      ]}
    />
  )
}
