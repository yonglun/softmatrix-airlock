package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

func seedKeyOrg(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path, is_key_holder) VALUES ('gw','网关组','/gw',true)`)
	require.NoError(t, err)
}

func sampleKey(id, hash string) *APIKey {
	return &APIKey{
		ID: id, KeyHash: hash, KeyPrefix: "ak-abcdefgh", OrgID: "gw",
		UserID: "u1", Name: "测试密钥", UpstreamKeyEnc: "enc-blob",
		Models: []string{"qwen-plus"},
	}
}

func TestKeyStoreCreatePendingThenActivate(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	seedKeyOrg(t, db)
	uid := seedUserID(t, db, "u1")
	s := NewPostgresKeyStore(db)
	ctx := context.Background()

	k := sampleKey("k1", "h1")
	k.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k))

	got, err := s.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status, "新建必须是 pending")
	require.Equal(t, []string{"qwen-plus"}, got.Models)

	require.NoError(t, s.MarkActive(ctx, "k1"))
	got, err = s.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, "active", got.Status)
}

func TestKeyStoreGetUnknownIsNotFound(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresKeyStore(db)

	_, err := s.Get(context.Background(), "nope")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}

func TestKeyStoreRevoke(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	seedKeyOrg(t, db)
	uid := seedUserID(t, db, "u1")
	s := NewPostgresKeyStore(db)
	ctx := context.Background()

	k := sampleKey("k1", "h1")
	k.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k))
	require.NoError(t, s.MarkActive(ctx, "k1"))
	require.NoError(t, s.Revoke(ctx, "k1"))

	got, err := s.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, "revoked", got.Status)
}

func TestKeyStoreListByOrgExcludesOtherNodes(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	seedKeyOrg(t, db)
	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path) VALUES ('other','别处','/other')`)
	require.NoError(t, err)
	uid := seedUserID(t, db, "u1")
	s := NewPostgresKeyStore(db)
	ctx := context.Background()

	k1 := sampleKey("k1", "h1")
	k1.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k1))
	k2 := sampleKey("k2", "h2")
	k2.OrgID = "other"
	k2.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k2))

	got, err := s.ListByOrg(ctx, "gw")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "k1", got[0].ID)
}

func TestKeyStoreListStalePending(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	seedKeyOrg(t, db)
	uid := seedUserID(t, db, "u1")
	s := NewPostgresKeyStore(db)
	ctx := context.Background()

	k1 := sampleKey("k1", "h1")
	k1.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k1))
	k2 := sampleKey("k2", "h2")
	k2.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k2))
	require.NoError(t, s.MarkActive(ctx, "k2"))

	// 把 k1 的创建时间推到过去，模拟卡住的 pending。
	_, err := db.ExecContext(ctx,
		`UPDATE api_keys SET created_at = now() - interval '1 hour' WHERE id='k1'`)
	require.NoError(t, err)

	got, err := s.ListStalePending(ctx, 10*time.Minute)
	require.NoError(t, err)
	require.Len(t, got, 1, "只有滞留超时的 pending 才算 stale")
	require.Equal(t, "k1", got[0].ID)
	require.Equal(t, "enc-blob", got[0].UpstreamKeyEnc, "清理需要上游密钥值")
}

func TestKeyStoreDelete(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	seedKeyOrg(t, db)
	uid := seedUserID(t, db, "u1")
	s := NewPostgresKeyStore(db)
	ctx := context.Background()

	k := sampleKey("k1", "h1")
	k.UserID = uid
	require.NoError(t, s.CreatePending(ctx, k))
	require.NoError(t, s.Delete(ctx, "k1"))

	_, err := s.Get(ctx, "k1")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}
