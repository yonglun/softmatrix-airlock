import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { App } from 'antd'
import { DangerZone } from './DangerZone'

function renderZone(canRevokeAll: boolean) {
  return render(
    <App>
      <DangerZone canRevokeAll={canRevokeAll} onDone={vi.fn()} />
    </App>,
  )
}

describe('DangerZone', () => {
  it('没有全局 key:revoke_all 时整个区块不出现', () => {
    renderZone(false)
    expect(screen.queryByText('危险操作')).not.toBeInTheDocument()
  })

  it('确认串没有原样打出来时按钮保持禁用——前端绝不代填这个串', () => {
    renderZone(true)
    expect(screen.getByRole('button', { name: /紧急吊销全系统密钥/ })).toBeDisabled()

    fireEvent.change(screen.getByLabelText('确认串'), { target: { value: 'revoke all keys' } })
    expect(screen.getByRole('button', { name: /紧急吊销全系统密钥/ })).toBeDisabled()
  })

  it('原样打出确认串后按钮才可用', () => {
    renderZone(true)
    fireEvent.change(screen.getByLabelText('确认串'), { target: { value: 'REVOKE ALL KEYS' } })
    expect(screen.getByRole('button', { name: /紧急吊销全系统密钥/ })).toBeEnabled()
  })
})
