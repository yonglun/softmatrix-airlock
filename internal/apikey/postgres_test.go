package apikey

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/migrations"
)

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

func seed(t *testing.T, db *sql.DB, c *cryptobox.Cipher, hash, status string, expires *time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM api_keys`, `DELETE FROM organizations WHERE id='gw'`, `DELETE FROM users`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO organizations (id, name, path) VALUES ('gw','网关组','/gw')`)
	require.NoError(t, err)
	var uid string
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO users (id, external_id, email, status) VALUES (gen_random_uuid(),'u1','u1@x.com','active') RETURNING id`).
		Scan(&uid))

	enc, err := c.Encrypt("sk-upstream-secret")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO api_keys (id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status, expires_at)
		VALUES ('k1',$1,'ak-abcdefgh','gw',$2,$3,$4,$5)`, hash, uid, enc, status, expires)
	require.NoError(t, err)
}

func testCipher(t *testing.T) *cryptobox.Cipher {
	t.Helper()
	c, err := cryptobox.NewCipher([]byte("airlock-dev-only-32byte-key!!!!!"))
	require.NoError(t, err)
	return c
}

func TestPostgresStoreReturnsDecryptedUpstreamKey(t *testing.T) {
	db := testDB(t)
	c := testCipher(t)
	seed(t, db, c, "hash-1", "active", nil)

	s := NewPostgresStore(db, c)
	k, err := s.ByHash(context.Background(), "hash-1")
	require.NoError(t, err)

	require.Equal(t, "sk-upstream-secret", k.UpstreamKey, "必须已解密")
	require.Equal(t, "gw", k.OrgID)
	require.Equal(t, StatusActive, k.Status)
}

func TestPostgresStoreUnknownHash(t *testing.T) {
	db := testDB(t)
	c := testCipher(t)
	seed(t, db, c, "hash-1", "active", nil)

	s := NewPostgresStore(db, c)
	_, err := s.ByHash(context.Background(), "nope")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestPostgresStorePendingKeyIsRejectedByValidate(t *testing.T) {
	// pending 表示上游尚未建成。既有的 Validate 是
	// 「非 active 一律拒绝」，因此 pending 天然被挡下。
	// 这条测试把这个性质钉住，防止日后有人把它改成逐个状态枚举而漏掉 pending。
	db := testDB(t)
	c := testCipher(t)
	seed(t, db, c, "hash-1", "pending", nil)

	s := NewPostgresStore(db, c)
	k, err := s.ByHash(context.Background(), "hash-1")
	require.NoError(t, err)
	require.Error(t, k.Validate(time.Now()), "pending 密钥必须不可用")
}

func TestPostgresStoreRevokedKeyIsRejected(t *testing.T) {
	db := testDB(t)
	c := testCipher(t)
	seed(t, db, c, "hash-1", "revoked", nil)

	s := NewPostgresStore(db, c)
	k, err := s.ByHash(context.Background(), "hash-1")
	require.NoError(t, err)
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyRevoked)
}

func TestPostgresStoreExpiredKeyIsRejected(t *testing.T) {
	db := testDB(t)
	c := testCipher(t)
	past := time.Now().Add(-time.Hour)
	seed(t, db, c, "hash-1", "active", &past)

	s := NewPostgresStore(db, c)
	k, err := s.ByHash(context.Background(), "hash-1")
	require.NoError(t, err)
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyExpired)
}
