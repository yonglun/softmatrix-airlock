/**
 * 工作台元数据。id 必须与后端 internal/control/workbench.go 里的常量一致——
 * 服务端决定「显示哪些」，这里只决定「显示成什么样」。
 */
export type Workbench = {
  id: string
  label: string
  /** 该工作台的落点路径 */
  home: string
  nav: { label: string; href: string }[]
}

export const WORKBENCHES: Workbench[] = [
  {
    id: 'my-space',
    label: '我的空间',
    home: '/my/requests',
    nav: [
      { label: '我的申请', href: '/my/requests' },
      { label: '我的密钥', href: '/my/keys' },
    ],
  },
  {
    id: 'platform',
    label: '平台管理',
    home: '/platform/orgs',
    nav: [
      { label: '组织与成员', href: '/platform/orgs' },
      { label: '虚拟密钥', href: '/platform/keys' },
    ],
  },
  {
    id: 'finops',
    label: '成本财务',
    home: '/finops/approvals',
    nav: [{ label: '提额审批', href: '/finops/approvals' }],
  },
]

/** 按服务端给的 id 列表挑出可见工作台，顺序以服务端为准。 */
export function visibleWorkbenches(ids: string[]): Workbench[] {
  return ids
    .map((id) => WORKBENCHES.find((w) => w.id === id))
    .filter((w): w is Workbench => w !== undefined)
}
