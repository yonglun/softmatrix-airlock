'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Popconfirm, Space, Table, Tag } from 'antd'
import { AppShell } from '@/components/AppShell'
import { PlaintextKeyModal } from '@/components/PlaintextKeyModal'
import { KeyRotateModal } from '@/components/KeyRotateModal'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import { KEY_STATUS, rotationCell } from '@/lib/keyDisplay'
import type { ApiKey, Org } from '@/lib/types'

export default function MyKeysPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [keys, setKeys] = useState<ApiKey[]>([])
  const [orgNames, setOrgNames] = useState<Record<string, string>>({})
  const [rotating, setRotating] = useState<ApiKey | null>(null)
  const [plaintext, setPlaintext] = useState<string | null>(null)

  const reload = useCallback(async () => {
    try {
      setKeys(await apiGet<ApiKey[]>('/api/keys/mine'))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }, [message])

  useEffect(() => {
    void reload()
    // 节点名只是显示用。普通开发者不一定看得到所有节点，
    // 取不到就退回显示原始 ID（与 P1.4a 同样的降级方式）。
    apiGet<Org[]>('/api/orgs')
      .then((list) => setOrgNames(Object.fromEntries(list.map((o) => [o.ID, o.Name]))))
      .catch(() => setOrgNames({}))
  }, [reload])

  const revoke = async (id: string) => {
    try {
      await apiSend('DELETE', `/api/keys/${id}`)
      message.success('已吊销')
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

  if (loading || !session) return null

  return (
    <AppShell workbenches={session.workbenches}>
      <Table<ApiKey>
        rowKey="id"
        dataSource={keys}
        pagination={false}
        locale={{ emptyText: '你名下还没有密钥' }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '前缀', dataIndex: 'key_prefix' },
          {
            title: '所属节点',
            dataIndex: 'org_id',
            render: (id: string) => orgNames[id] ?? id,
          },
          {
            title: '状态',
            dataIndex: 'status',
            render: (s: string) => {
              const meta = KEY_STATUS[s] ?? { text: s, color: 'default' }
              return <Tag color={meta.color}>{meta.text}</Tag>
            },
          },
          {
            title: '模型',
            dataIndex: 'models',
            render: (m: string[]) => (m.length > 0 ? m.join(', ') : '—'),
          },
          { title: '轮换状态', render: (_, k) => rotationCell(k) },
          {
            title: '操作',
            render: (_, k) =>
              k.status === 'active' ? (
                <Space>
                  <Button type="link" onClick={() => setRotating(k)}>
                    轮换
                  </Button>
                  <Popconfirm
                    title="确认吊销？"
                    description="吊销不可逆，使用这把密钥的客户端会立刻失效。密钥泄露时这是正确的止血手段。"
                    onConfirm={() => revoke(k.id)}
                  >
                    <Button type="link" danger>
                      吊销
                    </Button>
                  </Popconfirm>
                </Space>
              ) : null,
          },
        ]}
      />

      <KeyRotateModal
        target={rotating}
        onClose={() => setRotating(null)}
        onRotated={async (plain) => {
          setRotating(null)
          setPlaintext(plain)
          await reload()
        }}
      />

      <PlaintextKeyModal
        plaintext={plaintext}
        title="密钥已轮换"
        onClose={() => setPlaintext(null)}
      />
    </AppShell>
  )
}
