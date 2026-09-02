import type { NextConfig } from 'next'

// 静态导出：产物是纯静态文件，由 Go 二进制内嵌服务（设计文档 D1）。
// distDir 指向 dist/，与 web/embed.go 的 //go:embed all:dist 对应。
const config: NextConfig = {
  output: 'export',
  distDir: 'dist',
  // 静态导出没有服务端图片优化。
  images: { unoptimized: true },

  // 开发期把 API 与登录流量代理到 control。
  //
  // 这不只是图方便：登录回调走服务端的 sanitizeRedirect，它只吐同源
  // 「路径」、不带主机名。不代理的话登录完会落在 :8081 而不是 :3000，
  // 整个流程断掉。生产下前后端同源，这段 rewrites 不生效。
  async rewrites() {
    return [
      { source: '/api/:path*', destination: 'http://localhost:8081/api/:path*' },
      { source: '/auth/:path*', destination: 'http://localhost:8081/auth/:path*' },
    ]
  },
}

export default config
