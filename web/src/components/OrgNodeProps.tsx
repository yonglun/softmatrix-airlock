'use client'

import { useEffect, useState } from 'react'
import { App, Button, Card, Descriptions, Input, Select, Switch } from 'antd'
import { apiSend, ApiError } from '@/lib/api'
import type { Org } from '@/lib/types'

/**
 * 节点属性：改名、移动、密钥边界开关。
 *
 * 三个操作的守卫都在服务端，前端只如实转述它的中文（设计文档 D5）：
 * 移动成环、移动到无权的父节点（403）、节点下还有未吊销密钥时取消
 * 密钥边界（409）。
 */
export function OrgNodeProps({
  org,
  orgs,
  onChanged,
}: {
  org: Org
  orgs: Org[]
  onChanged: () => void | Promise<void>
}) {
  const { message } = App.useApp()
  const [name, setName] = useState(org.Name)
  const [parentID, setParentID] = useState<string | null>(org.ParentID)

  // 切换选中节点时把编辑中的值同步过来，否则会把上一个节点的名字带过来。
  useEffect(() => {
    setName(org.Name)
    setParentID(org.ParentID)
  }, [org.ID, org.Name, org.ParentID])

  const run = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      message.success(ok)
      await onChanged()
    } catch (e) {
      if (e instanceof ApiError) message.error(e.message)
      else message.error('操作失败')
    }
  }

  return (
    <Card title="节点属性" size="small">
      <Descriptions column={1} size="small">
        <Descriptions.Item label="名称">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            style={{ width: 220, marginRight: 8 }}
          />
          <Button
            disabled={name.trim() === '' || name === org.Name}
            onClick={() =>
              run(
                () => apiSend('PATCH', `/api/orgs/${org.ID}/name`, { name: name.trim() }),
                '已改名',
              )
            }
          >
            改名
          </Button>
        </Descriptions.Item>

        <Descriptions.Item label="父节点">
          <Select
            allowClear
            showSearch
            placeholder="（无父节点，即根节点）"
            style={{ width: 220, marginRight: 8 }}
            value={parentID ?? undefined}
            onChange={(v) => setParentID(v ?? null)}
            optionFilterProp="label"
            options={orgs
              .filter((o) => o.ID !== org.ID)
              .map((o) => ({ value: o.ID, label: o.Name }))}
          />
          <Button
            disabled={parentID === org.ParentID}
            onClick={() =>
              run(
                () => apiSend('PATCH', `/api/orgs/${org.ID}/parent`, { parent_id: parentID }),
                '已移动',
              )
            }
          >
            移动
          </Button>
        </Descriptions.Item>

        <Descriptions.Item label="密钥边界">
          <Switch
            checked={org.IsKeyHolder}
            onChange={(checked) =>
              run(
                () =>
                  apiSend('PUT', `/api/orgs/${org.ID}/key-holder`, { is_key_holder: checked }),
                checked ? '已标记为密钥边界' : '已取消密钥边界',
              )
            }
          />
        </Descriptions.Item>
      </Descriptions>
    </Card>
  )
}
