import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { GrantsTable } from './GrantsTable'
import type { EffectiveGrant, Org, Role, User } from '@/lib/types'

const users: User[] = [
  {
    ID: 'u1', ExternalID: 'e1', Email: 'a@x.com', DisplayName: '张三',
    Status: 'active', PrimaryOrgID: null,
  },
]
const roles: Role[] = [
  { ID: 'org_admin', Name: '组织管理员', Description: '', IsBuiltin: true },
]
const orgs: Org[] = [
  {
    ID: 'root', ParentID: null, Name: '总部', Path: '/root',
    ExternalSource: null, ExternalID: null, IsKeyHolder: false,
  },
]

function grant(over: Partial<EffectiveGrant>): EffectiveGrant {
  return {
    id: 'g1', user_id: 'u1', role_id: 'org_admin',
    source: 'direct', source_org_id: 'rd',
    created_at: '2026-09-03T00:00:00Z',
    ...over,
  }
}

function renderTable(grants: EffectiveGrant[]) {
  return render(
    <GrantsTable
      grants={grants}
      users={users}
      roles={roles}
      orgs={orgs}
      onRevoke={vi.fn()}
    />,
  )
}

describe('GrantsTable', () => {
  it('直授行才有撤销按钮', () => {
    renderTable([grant({ id: 'g-direct', source: 'direct' })])
    expect(screen.getByRole('button', { name: '撤销' })).toBeInTheDocument()
  })

  it('继承来的授予不给撤销按钮，只说清该去哪里撤销', () => {
    // 在这一页撤销一条继承来的授予，改动的是另一个节点上的行，
    // 会同时波及那个节点下所有其它子树。做成就手可点的按钮等于邀请误伤。
    renderTable([grant({ id: 'g-inherited', source: 'inherited', source_org_id: 'root' })])
    expect(screen.queryByRole('button', { name: '撤销' })).not.toBeInTheDocument()
    expect(screen.getByText(/需在「总部」上撤销/)).toBeInTheDocument()
  })

  it('全局授予同样不给撤销按钮', () => {
    renderTable([grant({ id: 'g-global', source: 'global', source_org_id: null })])
    expect(screen.queryByRole('button', { name: '撤销' })).not.toBeInTheDocument()
    expect(screen.getByText(/全局授予/)).toBeInTheDocument()
  })

  it('把 user_id 还原成人名，把 role_id 还原成角色名', () => {
    renderTable([grant({})])
    expect(screen.getByText('张三（a@x.com）')).toBeInTheDocument()
    expect(screen.getByText('组织管理员')).toBeInTheDocument()
  })
})
