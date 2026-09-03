'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Space, Table, Tag } from 'antd'
import { PlaintextKeyModal } from '@/components/PlaintextKeyModal'
import { AppShell } from '@/components/AppShell'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import type { ApiRequest, IssuedKey, Org } from '@/lib/types'

const STATUS_LABEL: Record<string, { text: string; color: string }> = {
  pending: { text: '待审批', color: 'gold' },
  approved: { text: '已批准', color: 'blue' },
  rejected: { text: '已驳回', color: 'red' },
  executed: { text: '已完成', color: 'green' },
  failed: { text: '执行失败', color: 'red' },
}

export default function MyRequestsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [rows, setRows] = useState<ApiRequest[]>([])
  const [orgs, setOrgs] = useState<Org[]>([])
  const [submitting, setSubmitting] = useState(false)
  const [claimed, setClaimed] = useState<IssuedKey | null>(null)
  const [form] = Form.useForm<{ org_id: string; key_name: string; reason: string }>()

  const reload = useCallback(async () => {
    try {
      setRows(await apiGet<ApiRequest[]>('/api/requests'))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }, [message])

  useEffect(() => {
    void reload()
    // 提交表单要选节点。可见范围由后端决定，这里只管展示。
    apiGet<Org[]>('/api/orgs').then(setOrgs).catch(() => setOrgs([]))
  }, [reload])

  const submit = async () => {
    const v = await form.validateFields()
    try {
      // models 必须显式传：后端把「没传」当作放行全部模型，
      // 这个 fail-open 只能在调用方堵住。这里先固定给一个具体模型。
      await apiSend('POST', '/api/requests', {
        kind: 'new_key',
        org_id: v.org_id,
        reason: v.reason,
        key_name: v.key_name,
        models: ['qwen-plus'],
      })
      message.success('已提交，等待审批')
      setSubmitting(false)
      form.resetFields()
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

  const claim = async (id: string) => {
    try {
      const key = await apiSend<IssuedKey>('POST', `/api/requests/${id}/claim`)
      setClaimed(key)
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

  if (loading || !session) return null

  return (
    <AppShell workbenches={session.workbenches}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Button type="primary" onClick={() => setSubmitting(true)}>
          申请新密钥
        </Button>

        <Table<ApiRequest>
          rowKey="id"
          dataSource={rows}
          pagination={false}
          columns={[
            { title: '名称', dataIndex: 'key_name', render: (v) => v ?? '—' },
            { title: '节点', dataIndex: 'org_id' },
            { title: '理由', dataIndex: 'reason' },
            {
              title: '状态',
              dataIndex: 'status',
              render: (s: string) => {
                const meta = STATUS_LABEL[s] ?? { text: s, color: 'default' }
                return <Tag color={meta.color}>{meta.text}</Tag>
              },
            },
            {
              title: '操作',
              render: (_, r) =>
                r.status === 'approved' ? (
                  <Button type="link" onClick={() => claim(r.id)}>
                    领取密钥
                  </Button>
                ) : null,
            },
          ]}
        />
      </Space>

      <Modal
        title="申请新密钥"
        open={submitting}
        onCancel={() => setSubmitting(false)}
        onOk={submit}
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="org_id"
            label="所属节点"
            rules={[{ required: true, message: '请选择所属节点' }]}
          >
            <Input list="org-ids" placeholder="填写节点 ID" />
          </Form.Item>
          <datalist id="org-ids">
            {orgs.map((o) => (
              <option key={o.ID} value={o.ID}>
                {o.Name}
              </option>
            ))}
          </datalist>
          <Form.Item
            name="key_name"
            label="密钥名称"
            rules={[{ required: true, message: '请输入密钥名称' }]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            name="reason"
            label="申请理由"
            rules={[{ required: true, message: '请填写申请理由' }]}
          >
            <Input.TextArea rows={3} />
          </Form.Item>
        </Form>
      </Modal>

      <PlaintextKeyModal
        plaintext={claimed?.key ?? null}
        title="密钥已签发"
        onClose={() => setClaimed(null)}
      />
    </AppShell>
  )
}
