import type { Metadata } from 'next'
import { App as AntdApp, ConfigProvider } from 'antd'
import { AntdRegistry } from '@ant-design/nextjs-registry'
import zhCN from 'antd/locale/zh_CN'
import { SessionProvider } from '@/lib/session'

export const metadata: Metadata = { title: 'Airlock 控制台' }

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN">
      <body>
        <AntdRegistry>
          {/* 界面中文优先：antd 的 zh_CN 是官方内置的，
              用户可见文案集中在各组件里，将来加第二语言时替换这一层。 */}
          <ConfigProvider locale={zhCN}>
            <AntdApp>
              <SessionProvider>{children}</SessionProvider>
            </AntdApp>
          </ConfigProvider>
        </AntdRegistry>
      </body>
    </html>
  )
}
