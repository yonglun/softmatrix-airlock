'use client'

import { Layout, Menu } from 'antd'
import { usePathname, useRouter } from 'next/navigation'
import { visibleWorkbenches } from './workbenches'

const { Header, Sider, Content } = Layout

/**
 * 顶部切换工作台、左侧是该工作台的导航。
 *
 * workbenches 由服务端的 whoami 给出——前端不自行判定谁能看到什么
 * （设计文档 D2）。前端不认识的 id 会被忽略而不是崩掉：服务端加了
 * 新工作台而前端还没跟上时，不该白屏。
 */
export function AppShell({
  workbenches,
  children,
}: {
  workbenches: string[]
  children: React.ReactNode
}) {
  const pathname = usePathname()
  const router = useRouter()
  const visible = visibleWorkbenches(workbenches)

  const current = visible.find((w) => pathname.startsWith('/' + w.home.split('/')[1])) ?? visible[0]

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ display: 'flex', alignItems: 'center' }}>
        <div style={{ color: '#fff', marginRight: 32, fontWeight: 600 }}>Airlock</div>
        <Menu
          theme="dark"
          mode="horizontal"
          selectedKeys={current ? [current.id] : []}
          items={visible.map((w) => ({ key: w.id, label: w.label }))}
          onClick={({ key }) => {
            const w = visible.find((x) => x.id === key)
            if (w) router.push(w.home)
          }}
          style={{ flex: 1, minWidth: 0 }}
        />
      </Header>
      <Layout>
        <Sider width={200} theme="light">
          <Menu
            mode="inline"
            selectedKeys={[pathname]}
            items={(current?.nav ?? []).map((n) => ({ key: n.href, label: n.label }))}
            onClick={({ key }) => router.push(String(key))}
            style={{ height: '100%' }}
          />
        </Sider>
        <Content style={{ padding: 24 }}>{children}</Content>
      </Layout>
    </Layout>
  )
}
