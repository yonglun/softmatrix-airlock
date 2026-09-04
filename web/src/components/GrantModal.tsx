'use client'

import { useCallback, useEffect, useState } from 'react'
import { App, Modal, Select, Typography } from 'antd'
import { apiGet, apiSend, ApiError } from '@/lib/api'
import { userLabel } from '@/lib/users'
import type { Role, User } from '@/lib/types'

/**
 * 授予角色弹窗。
 *
 * 角色下拉来自 GET /api/roles?grantable_at=<节点>——服务端已经用反提权闸
 * 滤掉了调用者授不了的角色，所以这里不会出现注定拿 403 的选项。前端不自己
 * 算这个：角色接口不返回权限集，算不出来（设计文档 D2）。
 */
export function GrantModal({
  orgID,
  orgName,
  users,
  onClose,
  onGranted,
}: {
  orgID: string | null
  orgName: string
  users: User[]
  onClose: () => void
  onGranted: () => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [roles, setRoles] = useState<Role[]>([])
  const [userID, setUserID] = useState<string | undefined>()
  const [roleID, setRoleID] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)

  const loadRoles = useCallback(async () => {
    if (!orgID) return
    try {
      setRoles(await apiGet<Role[]>(`/api/roles?grantable_at=${orgID}`))
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      setRoles([])
    }
  }, [orgID, message])

  useEffect(() => {
    void loadRoles()
  }, [loadRoles])

  const submit = async () => {
    if (!orgID || !userID || !roleID) return
    setBusy(true)
    try {
      await apiSend('POST', '/api/grants', {
        user_id: userID,
        role_id: roleID,
        org_id: orgID,
      })
      message.success('已授予')
      setUserID(undefined)
      setRoleID(undefined)
      await onGranted()
      onClose()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('授予失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`在「${orgName}」上授予角色`}
      open={orgID !== null}
      onCancel={onClose}
      onOk={submit}
      okButtonProps={{ disabled: !userID || !roleID }}
      confirmLoading={busy}
    >
      <Typography.Paragraph type="secondary">
        下拉里只有你在这个节点上能授予的角色——授不了的已由服务端滤掉。
      </Typography.Paragraph>
      <Select
        showSearch
        style={{ width: '100%', marginBottom: 12 }}
        placeholder="选择用户"
        value={userID}
        onChange={setUserID}
        optionFilterProp="label"
        options={users.map((u) => ({ value: u.ID, label: userLabel(u, u.ID) }))}
      />
      <Select
        style={{ width: '100%' }}
        placeholder="选择角色"
        value={roleID}
        onChange={setRoleID}
        options={roles.map((r) => ({ value: r.ID, label: r.Name }))}
      />
    </Modal>
  )
}
