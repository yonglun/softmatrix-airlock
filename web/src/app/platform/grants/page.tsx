'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Button, Space } from 'antd'
import { AppShell } from '@/components/AppShell'
import { OrgTree } from '@/components/OrgTree'
import { GrantsTable } from '@/components/GrantsTable'
import { GrantModal } from '@/components/GrantModal'
import { useSession } from '@/lib/session'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import type { EffectiveGrant, Org, Role, User } from '@/lib/types'

export default function GrantsPage() {
  const { session, loading } = useSession()
  const { message } = App.useApp()
  const [orgs, setOrgs] = useState<Org[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [grants, setGrants] = useState<EffectiveGrant[]>([])
  const [granting, setGranting] = useState(false)

  useEffect(() => {
    apiGet<Org[]>('/api/orgs')
      .then(setOrgs)
      .catch(() => setOrgs([]))
    // 用户与角色只用来把 ID 还原成人话，取不到就退回显示原始 ID。
    apiGet<User[]>('/api/users')
      .then(setUsers)
      .catch(() => setUsers([]))
    apiGet<Role[]>('/api/roles')
      .then(setRoles)
      .catch(() => setRoles([]))
  }, [])

  const reload = useCallback(async () => {
    if (!selected) {
      setGrants([])
      return
    }
    try {
      // 有效视图：直授 + 继承 + 全局。只问直授的话，页面会漏掉
      // 祖先上的 org_admin 与全局 platform_admin——而他们权限最大。
      setGrants(await apiGet<EffectiveGrant[]>(`/api/orgs/${selected}/effective-grants`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setGrants([])
    }
  }, [selected, message])

  useEffect(() => {
    void reload()
  }, [reload])

  const revoke = async (id: string) => {
    try {
      await apiSend('DELETE', `/api/grants/${id}`)
      message.success('已撤销')
      await reload()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
    }
  }

  if (loading || !session) return null

  const selectedName = orgs.find((o) => o.ID === selected)?.Name ?? ''

  return (
    <AppShell workbenches={session.workbenches}>
      <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
        <div style={{ width: 260, flexShrink: 0 }}>
          <OrgTree orgs={orgs} selected={selected} onSelect={setSelected} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <Space direction="vertical" style={{ width: '100%' }} size="middle">
            <Button type="primary" disabled={!selected} onClick={() => setGranting(true)}>
              授予角色
            </Button>

            {selected ? (
              <GrantsTable
                grants={grants}
                users={users}
                roles={roles}
                orgs={orgs}
                onRevoke={revoke}
              />
            ) : (
              <div style={{ color: '#999' }}>请先在左侧选择一个节点</div>
            )}
          </Space>
        </div>
      </div>

      <GrantModal
        orgID={granting ? selected : null}
        orgName={selectedName}
        users={users}
        onClose={() => setGranting(false)}
        onGranted={reload}
      />
    </AppShell>
  )
}
