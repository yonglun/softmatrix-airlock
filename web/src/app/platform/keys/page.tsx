'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Popconfirm, Space, Switch, Table, Tag } from 'antd'
import { AppShell } from '@/components/AppShell'
import { OrgTree } from '@/components/OrgTree'
import { PlaintextKeyModal } from '@/components/PlaintextKeyModal'
import { KeyRotateModal } from '@/components/KeyRotateModal'
import { SubtreeRevokeModal } from '@/components/SubtreeRevokeModal'
import { DangerZone } from '@/components/DangerZone'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import { KEY_STATUS, rotationCell } from '@/lib/keyDisplay'
import type { ApiKey, IssuedKey, Org } from '@/lib/types'

export default function PlatformKeysPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [orgs, setOrgs] = useState<Org[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [subtree, setSubtree] = useState(false)
  const [keys, setKeys] = useState<ApiKey[]>([])
  const [issuing, setIssuing] = useState(false)
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [rotating, setRotating] = useState<ApiKey | null>(null)
  const [revokingSubtree, setRevokingSubtree] = useState(false)
  const [form] = Form.useForm<{ user_id: string; name: string; models: string }>()

  useEffect(() => {
    apiGet<Org[]>('/api/orgs')
      .then(setOrgs)
      .catch(() => setOrgs([]))
  }, [])

  const reload = useCallback(async () => {
    if (!selected) {
      setKeys([])
      return
    }
    try {
      // subtree=true 时服务端用的是与子树吊销逐字相同的节点选择子句，
      // 因此这份列表同时也是那个操作的影响范围。
      const q = subtree ? '?subtree=true' : ''
      setKeys(await apiGet<ApiKey[]>(`/api/orgs/${selected}/keys${q}`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setKeys([])
    }
  }, [selected, subtree, message])

  useEffect(() => {
    void reload()
  }, [reload])

  const issue = async () => {
    const v = await form.validateFields()
    try {
      // models 必须显式传：后端把「没传」当作放行全部模型，
      // 这个 fail-open 只能在调用方堵住（与 P1.4a 的申请页同一处理）。
      const models = v.models
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      const issued = await apiSend<IssuedKey>('POST', '/api/keys', {
        org_id: selected,
        user_id: v.user_id,
        name: v.name,
        models,
      })
      setIssuing(false)
      form.resetFields()
      setPlaintext(issued.key)
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

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
      <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
        <div style={{ width: 260, flexShrink: 0 }}>
          <OrgTree orgs={orgs} selected={selected} onSelect={setSelected} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <Space direction="vertical" style={{ width: '100%' }} size="middle">
            <Space>
              <Button type="primary" disabled={!selected} onClick={() => setIssuing(true)}>
                签发密钥
              </Button>
              <span>
                包含子树 <Switch checked={subtree} onChange={setSubtree} disabled={!selected} />
              </span>
              <Popconfirm
                title="先看一眼影响范围"
                description="下一步会列出这个子树下的全部密钥。"
                onConfirm={() => setRevokingSubtree(true)}
              >
                <Button danger disabled={!selected}>
                  吊销该子树的全部密钥
                </Button>
              </Popconfirm>
            </Space>

            <Table<ApiKey>
              rowKey="id"
              dataSource={keys}
              pagination={false}
              locale={{ emptyText: selected ? '该范围下没有密钥' : '请先在左侧选择一个节点' }}
              columns={[
                { title: '名称', dataIndex: 'name' },
                { title: '前缀', dataIndex: 'key_prefix' },
                { title: '责任人', dataIndex: 'user_id' },
                { title: '所属节点', dataIndex: 'org_id' },
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
                          title="确认吊销该密钥？"
                          description="吊销不可逆，使用它的客户端会立刻失效。"
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
          </Space>

          <DangerZone
            canRevokeAll={session.global_permissions.includes('key:revoke_all')}
            onDone={reload}
          />
        </div>
      </div>

      <Modal title="签发密钥" open={issuing} onCancel={() => setIssuing(false)} onOk={issue}>
        <Form form={form} layout="vertical">
          <Form.Item
            name="user_id"
            label="责任人用户 ID"
            rules={[{ required: true, message: '请填写责任人用户 ID' }]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            name="name"
            label="密钥名称"
            rules={[{ required: true, message: '请输入密钥名称' }]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            name="models"
            label="可用模型（逗号分隔）"
            rules={[{ required: true, message: '必须显式指定模型' }]}
          >
            <Input placeholder="qwen-plus" />
          </Form.Item>
        </Form>
      </Modal>

      <SubtreeRevokeModal
        org={revokingSubtree ? (orgs.find((o) => o.ID === selected) ?? null) : null}
        onClose={() => setRevokingSubtree(false)}
        onDone={reload}
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
        title="密钥明文"
        onClose={() => setPlaintext(null)}
      />
    </AppShell>
  )
}
