// Command airlock 是 Airlock 的唯一分发物。
// 通过子命令区分进程角色：edge（数据面）、control（管理面）、migrate（数据库迁移）。
// 单二进制分发是为了杜绝私有化交付时 edge 与 control 版本错配。
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/softmatrix/airlock/internal/app"
)

const usage = `airlock —— 企业 AI 网关

用法：
  airlock edge      启动数据面（ak- 鉴权、转发、计费）
  airlock control   启动管理面（OIDC 登录、组织树、成员）
  airlock migrate   执行数据库迁移后退出
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "edge":
		err = app.RunEdge()
	case "control":
		err = app.RunControl()
	case "migrate":
		err = app.RunMigrate()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		slog.Error("airlock 退出", "subcommand", os.Args[1], "err", err)
		os.Exit(1)
	}
}
