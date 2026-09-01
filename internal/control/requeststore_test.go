package control

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedRequestFixtures 造一个 key holder 节点、一个真实用户与一把密钥。
// api_keys.user_id 与 requests.requester_id 上都有指向 users 的外键，
// 字面量 id 会被拒绝。
func seedRequestFixtures(t *testing.T, db *sql.DB) (uid, keyID string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path, is_key_holder) VALUES ('gw','网关组','/gw',true)`)
	require.NoError(t, err)
	uid = seedUserID(t, db, "req-fixture-user")
	keyID = "k-fixture"
	_, err = db.Exec(`
		INSERT INTO api_keys (id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status)
		VALUES ($1,'h-fixture','ak-xxx','gw',$2,'enc','active')`, keyID, uid)
	require.NoError(t, err)
	return uid, keyID
}

func TestRequestsTableAcceptsNewKeyShape(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)

	_, err := db.Exec(`
		INSERT INTO requests (id, kind, requester_id, org_id, reason, key_name, models)
		VALUES ('r1','new_key',$1,'gw','要一把密钥','我的密钥','["qwen-plus"]'::jsonb)`, uid)
	require.NoError(t, err)

	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM requests WHERE id='r1'`).Scan(&status))
	require.Equal(t, "pending", status, "新建申请默认 pending")
}

func TestRequestsTableAcceptsQuotaBumpShape(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)

	_, err := db.Exec(`
		INSERT INTO requests (id, kind, requester_id, org_id, target_key_id,
		                      bump_to_budget, bump_expires_at)
		VALUES ('r2','quota_bump',$1,'gw',$2, 50, now() + interval '7 days')`, uid, keyID)
	require.NoError(t, err)
}

func TestRequestsCheckRejectsNewKeyWithoutModels(t *testing.T) {
	// CHECK 约束是这张表「一表两形状」的唯一保障。
	// 少了它，kind 与实际填的列就可能对不上，而代码里没人会发现。
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)

	_, err := db.Exec(`
		INSERT INTO requests (id, kind, requester_id, org_id, key_name)
		VALUES ('r3','new_key',$1,'gw','缺 models')`, uid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requests_kind_shape_check")
}

func TestRequestsCheckRejectsCrossKindColumns(t *testing.T) {
	// new_key 不该带 target_key_id，quota_bump 不该带 key_name。
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)

	_, err := db.Exec(`
		INSERT INTO requests (id, kind, requester_id, org_id, key_name, models, target_key_id)
		VALUES ('r4','new_key',$1,'gw','x','[]'::jsonb,$2)`, uid, keyID)
	require.Error(t, err, "new_key 带 target_key_id 必须被拒")
	require.Contains(t, err.Error(), "requests_kind_shape_check")
}

func TestNotificationsChannelOnlyAcceptsEmail(t *testing.T) {
	// 这条约束是「拿不到真实租户就不声称支持」的物理兑现：
	// 在钉钉/企微实测之前，系统存不下一条声称由它们投递的记录。
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)
	_, err := db.Exec(`
		INSERT INTO requests (id, kind, requester_id, org_id, key_name, models)
		VALUES ('r5','new_key',$1,'gw','x','[]'::jsonb)`, uid)
	require.NoError(t, err)

	_, err = db.Exec(`
		INSERT INTO notifications (id, request_id, event, channel, recipient, subject, body)
		VALUES ('n1','r5','submitted','dingtalk','someone','s','b')`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "notifications_channel_check")
}
