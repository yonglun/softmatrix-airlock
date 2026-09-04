'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Popconfirm, Space } from 'antd'
import { AppShell } from '@/components/AppShell'
import { OrgTree } from '@/components/OrgTree'
import { OrgNodeProps } from '@/components/OrgNodeProps'
import { OrgMembers } from '@/components/OrgMembers'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import type { Org, User } from '@/lib/types'

export default function OrgsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [orgs, setOrgs] = useState<Org[]>([])
  const [users, setUsers] = useState<User[]>([])
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

  // 成员块要把 user_id 还原成人名并列出该节点的人。取不到就退回空列表，
  // 页面其余部分照常可用。
  const reloadUsers = useCallback(async () => {
    try {
      setUsers(await apiGet<User[]>('/api/users'))
    } catch {
      setUsers([])
    }
  }, [])

  useEffect(() => {
    void reloadUsers()
  }, [reloadUsers])

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

  const selectedOrg = orgs.find((o) => o.ID === selected) ?? null

  if (loading || !session) return null

  return (
    <AppShell workbenches={session.workbenches}>
      <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
        <Space direction="vertical" style={{ width: 320, flexShrink: 0 }} size="middle">
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

        <div style={{ flex: 1, minWidth: 0 }}>
          {selectedOrg ? (
            <>
              <OrgNodeProps org={selectedOrg} orgs={orgs} onChanged={reload} />
              <OrgMembers org={selectedOrg} users={users} onChanged={reloadUsers} />
            </>
          ) : (
            <div style={{ color: '#999' }}>选择一个节点以查看属性与成员</div>
          )}
        </div>
      </div>

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
