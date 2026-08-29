package app

import (
	"database/sql"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/softmatrix/airlock/internal/config"
	"github.com/softmatrix/airlock/migrations"
)

// RunMigrate 只执行数据库迁移后退出。
func RunMigrate() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		return err
	}
	slog.Info("迁移完成")
	return nil
}
