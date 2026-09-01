package control

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationAddsPendingStatusAndQuotaColumns(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	ctx := context.Background()

	_, err := db.ExecContext(ctx,
		`INSERT INTO organizations (id, name, path) VALUES ('gw', '网关组', '/gw')`)
	require.NoError(t, err)
	uid := seedUserID(t, db, "u1")

	// pending 必须被 CHECK 约束接受，配额列必须存在。
	_, err = db.ExecContext(ctx, `
		INSERT INTO api_keys
			(id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status,
			 models, max_budget, budget_duration, rpm_limit, tpm_limit)
		VALUES ('k1','h1','ak-xxx','gw',$1,'enc','pending',
		        '["qwen-plus"]'::jsonb, 10.5, '30d', 60, 10000)`, uid)
	require.NoError(t, err)

	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM api_keys WHERE id='k1'`).Scan(&status))
	require.Equal(t, "pending", status)
}

func TestMigrationStillRejectsUnknownStatus(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	ctx := context.Background()

	_, err := db.ExecContext(ctx,
		`INSERT INTO organizations (id, name, path) VALUES ('gw', '网关组', '/gw')`)
	require.NoError(t, err)
	uid := seedUserID(t, db, "u2")

	// 用一个真实存在的 user_id，确保失败原因确实是 CHECK 约束，
	// 而不是恰好也会失败的外键。
	_, err = db.ExecContext(ctx, `
		INSERT INTO api_keys (id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status)
		VALUES ('k2','h2','ak-xxx','gw',$1,'enc','bogus')`, uid)
	require.Error(t, err, "CHECK 约束必须仍然挡住未知状态")
	require.Contains(t, err.Error(), "api_keys_status_check")
}
