package control

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/migrations"
)

// testDB 连上本地 Postgres 并把 schema 迁到最新。
// 未设置 POSTGRES_DSN 时跳过——保证没起数据库的环境下 go test ./... 仍然全绿。
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 POSTGRES_DSN，跳过需要数据库的集成测试")
	}

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	require.NoError(t, migrations.Up(db))

	t.Cleanup(func() { _ = db.Close() })
	return db
}

// cleanTables 清空本包测试涉及的表。按外键依赖顺序删。
func cleanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		// notifications 外键指向 requests，requests 指向 users/organizations/api_keys，
		// 因此这两张必须排在 api_keys 与 users 之前删，否则外键违反。
		`DELETE FROM notifications`,
		`DELETE FROM requests`,
		`DELETE FROM role_grants`,
		`DELETE FROM api_keys`,
		`DELETE FROM sessions`,
		`DELETE FROM login_states`,
		`DELETE FROM users`,
		`DELETE FROM organizations`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, "清表失败: %s", stmt)
	}
}
