// Package migrations 内嵌 SQL 迁移文件并提供运行入口。
// 内嵌而非依赖外部 CLI，是为了让私有化交付时只需分发单个二进制。
package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var fs embed.FS

// Up 把数据库迁移到最新版本。
func Up(db *sql.DB) error {
	goose.SetBaseFS(fs)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("设置 goose 方言失败: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("执行迁移失败: %w", err)
	}
	return nil
}
