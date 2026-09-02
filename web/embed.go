// Package web 承载控制台的构建产物。
//
// 它只做一件事：把 next build 的输出嵌进二进制。不含任何业务逻辑，
// 因此 internal/control 依赖它是安全的——但反过来不行。
package web

import (
	"embed"
	"io/fs"
)

// all: 前缀会把点开头的文件也纳入，因此 dist/.gitkeep 能保证
// 前端未构建时 embed 也匹配得到东西、go build 不至于失败。
//
//go:embed all:dist
var dist embed.FS

// Dist 返回以 dist/ 为根的文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// 只有 dist 目录不存在才会走到这里，而它由 .gitkeep 保证存在。
		panic(err)
	}
	return sub
}
