'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Popconfirm, Space } from 'antd'
import { AppShell } from '@/components/AppShell'
import { OrgTree } from '@/components/OrgTree'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import type { Org } from '@/lib/types'

export default function OrgsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [orgs, setOrgs] = useState<Org[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [form] = Form.useForm<{ name: string }>()

  const reload = useCallback(async () => {
    try {
      setOrgs(await apiGet<Org[]>('/api/orgs'))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }, [message])

  useEffect(() => {
    void reload()
  }, [reload])

  // 后端的错误文案本来就是写给人看的中文，直接显示（设计文档 D5）。
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      message.success(ok)
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('操作失败')
    }
  }

  if (loading || !session) return null

  return (
    <AppShell workbenches={session.workbenches}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Space>
          <Button type="primary" onClick={() => setCreating(true)}>
            新建节点
          </Button>
          <Popconfirm
            title="确认删除该节点？"
            disabled={!selected}
            onConfirm={() =>
              run(() => apiSend('DELETE', `/api/orgs/${selected}`), '已删除')
            }
          >
            <Button danger disabled={!selected}>
              删除所选
            </Button>
          </Popconfirm>
        </Space>

        <OrgTree orgs={orgs} selected={selected} onSelect={setSelected} />
      </Space>

      <Modal
        title="新建组织节点"
        open={creating}
        onCancel={() => setCreating(false)}
        onOk={async () => {
          const v = await form.validateFields()
          await run(
            () => apiSend('POST', '/api/orgs', { name: v.name, parent_id: selected }),
            '已创建',
          )
          form.resetFields()
          setCreating(false)
        }}
      >
        <Form form={form} layout="vertical">
          <Form.Item
            name="name"
            label="名称"
            rules={[{ required: true, message: '请输入节点名称' }]}
          >
            <Input placeholder={selected ? '将建在所选节点下' : '将建为根节点'} />
          </Form.Item>
        </Form>
      </Modal>
    </AppShell>
  )
}
