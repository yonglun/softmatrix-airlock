package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

func newKeyReq(id, uid string) *Request {
	return &Request{
		ID: id, Kind: RequestKindNewKey, RequesterID: uid, OrgID: "gw",
		Reason: "要一把密钥", KeyName: strp("我的密钥"), Models: []string{"qwen-plus"},
	}
}

func bumpReq(id, uid, keyID string, expires time.Time) *Request {
	to := 50.0
	return &Request{
		ID: id, Kind: RequestKindQuotaBump, RequesterID: uid, OrgID: "gw",
		Reason: "临时活动", TargetKeyID: &keyID, BumpToBudget: &to, BumpExpiresAt: &expires,
	}
}

func TestRequestStoreCreateAndGet(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()

	req := newKeyReq("r1", uid)
	require.NoError(t, s.Create(ctx, req))
	require.False(t, req.CreatedAt.IsZero(),
		"Create 必须把数据库算出的 created_at 读回调用方的结构体")

	got, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, RequestStatusPending, got.Status)
	require.Equal(t, []string{"qwen-plus"}, got.Models)
	require.Equal(t, "我的密钥", *got.KeyName)
}

func TestRequestStoreGetUnknown(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRequestStore(db)

	_, err := s.Get(context.Background(), "nope")
	require.ErrorIs(t, err, ErrRequestNotFound)
}

func TestRequestStoreDecideOnlyFromPending(t *testing.T) {
	// 重复审批必须被挡住：两个管理员同时点批准，第二次应该失败
	// 而不是把 decided_by 覆盖成后点的那个人。
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, newKeyReq("r1", uid)))

	require.NoError(t, s.Decide(ctx, "r1", RequestStatusApproved, uid))

	err := s.Decide(ctx, "r1", RequestStatusRejected, uid)
	require.ErrorIs(t, err, ErrRequestNotPending, "已审批的单子不能再审一次")

	got, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, RequestStatusApproved, got.Status, "第一次的结论必须保住")
	require.NotNil(t, got.DecidedAt)
}

func TestRequestStoreMarkExecuted(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, newKeyReq("r1", uid)))
	require.NoError(t, s.Decide(ctx, "r1", RequestStatusApproved, uid))

	prev := 5.0
	require.NoError(t, s.MarkExecuted(ctx, "r1", &keyID, &prev))

	got, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, RequestStatusExecuted, got.Status)
	require.Equal(t, keyID, *got.IssuedKeyID)
	require.Equal(t, 5.0, *got.PrevBudget)
	require.NotNil(t, got.ExecutedAt)
}

func TestRequestStoreListByRequester(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, _ := seedRequestFixtures(t, db)
	other := seedUserID(t, db, "someone-else")
	s := NewPostgresRequestStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, newKeyReq("mine", uid)))
	require.NoError(t, s.Create(ctx, newKeyReq("theirs", other)))

	got, err := s.ListByRequester(ctx, uid)
	require.NoError(t, err)
	require.Len(t, got, 1, "只该看到自己发起的")
	require.Equal(t, "mine", got[0].ID)
}

func TestRequestStoreRecordAttemptAndMarkFailed(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, bumpReq("r1", uid, keyID, time.Now().Add(time.Hour))))
	require.NoError(t, s.Decide(ctx, "r1", RequestStatusApproved, uid))

	require.NoError(t, s.RecordAttempt(ctx, "r1", "上游超时"))
	got, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, 1, got.Attempts)
	require.Equal(t, "上游超时", *got.LastError)
	require.Equal(t, RequestStatusApproved, got.Status, "计一次尝试不改变状态")

	require.NoError(t, s.MarkFailed(ctx, "r1", "重试耗尽"))
	got, err = s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, RequestStatusFailed, got.Status)
}

func TestRequestStoreListApprovedBumps(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, bumpReq("r-bump", uid, keyID, time.Now().Add(24*time.Hour))))
	require.NoError(t, s.Decide(ctx, "r-bump", RequestStatusApproved, uid))
	// 一张已批准的 new_key 不该被提额执行器捞到。
	require.NoError(t, s.Create(ctx, newKeyReq("r-key", uid)))
	require.NoError(t, s.Decide(ctx, "r-key", RequestStatusApproved, uid))

	got, err := s.ListApprovedBumps(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "r-bump", got[0].ID)
}

func TestRequestStoreListExpiredBumps(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	uid, keyID := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)
	ctx := context.Background()
	now := time.Now()

	// 已过期、已执行、未回收 —— 该被捞到
	require.NoError(t, s.Create(ctx, bumpReq("r-old", uid, keyID, now.Add(-time.Hour))))
	require.NoError(t, s.Decide(ctx, "r-old", RequestStatusApproved, uid))
	prev := 5.0
	require.NoError(t, s.MarkExecuted(ctx, "r-old", nil, &prev))

	// 未过期 —— 不该碰
	require.NoError(t, s.Create(ctx, bumpReq("r-live", uid, keyID, now.Add(time.Hour))))
	require.NoError(t, s.Decide(ctx, "r-live", RequestStatusApproved, uid))
	require.NoError(t, s.MarkExecuted(ctx, "r-live", nil, &prev))

	got, err := s.ListExpiredBumps(ctx, now)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "r-old", got[0].ID)

	require.NoError(t, s.MarkReclaimed(ctx, "r-old"))
	got, err = s.ListExpiredBumps(ctx, now)
	require.NoError(t, err)
	require.Empty(t, got, "已回收的不该被重复捞到")
}

func TestRequestStoreMarkExecutedOnlyFromApproved(t *testing.T) {
	// 两个并发的领取都会看到 approved。只有一个能赢下这次转换，
	// 否则一次审批就换来了两把密钥。
	db := testDB(t)
	cleanTables(t, db)
	ctx := context.Background()
	uid, keyID := seedRequestFixtures(t, db)
	s := NewPostgresRequestStore(db)

	require.NoError(t, s.Create(ctx, newKeyReq("r1", uid)))
	require.NoError(t, s.Decide(ctx, "r1", RequestStatusApproved, uid))

	require.NoError(t, s.MarkExecuted(ctx, "r1", &keyID, nil))

	err := s.MarkExecuted(ctx, "r1", &keyID, nil)
	require.ErrorIs(t, err, ErrRequestNotApproved, "第二次必须输掉这次转换")
}

func TestRequestStoreMarkExecutedUnknownRequest(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	s := NewPostgresRequestStore(db)

	err := s.MarkExecuted(context.Background(), "nope", nil, nil)
	require.ErrorIs(t, err, ErrRequestNotFound, "不存在与已执行必须能分辨")
}

// enqueueOne 是这组测试共用的准备动作：一张申请单 + 一条待发通知。
func enqueueOne(t *testing.T, db *sql.DB) (rs *postgresRequestStore, ns *postgresNotificationStore) {
	t.Helper()
	ctx := context.Background()
	uid, _ := seedRequestFixtures(t, db)
	rs = NewPostgresRequestStore(db)
	ns = NewPostgresNotificationStore(db)
	require.NoError(t, rs.Create(ctx, newKeyReq("r1", uid)))
	require.NoError(t, ns.Enqueue(ctx, &Notification{
		ID: "n1", RequestID: "r1", Event: NotifyEventSubmitted, Channel: "email",
		Recipient: "boss@x.com", Subject: "待审", Body: "有一张新申请",
	}))
	return rs, ns
}

func TestNotificationStoreEnqueueAndListPending(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	_, ns := enqueueOne(t, db)

	got, err := ns.ListPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "boss@x.com", got[0].Recipient)
	require.Equal(t, "pending", got[0].Status)
}

func TestNotificationStoreMarkSentRemovesFromPending(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	_, ns := enqueueOne(t, db)
	ctx := context.Background()

	require.NoError(t, ns.MarkSent(ctx, "n1"))

	got, err := ns.ListPending(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, got, "已送达的不该再被投递")
}

func TestNotificationStoreRecordFailureKeepsItPending(t *testing.T) {
	// 投递失败要留在队列里等下一轮，而不是被丢弃——
	// 静默丢一封通知意味着审批人永远不知道有待审申请。
	db := testDB(t)
	cleanTables(t, db)
	_, ns := enqueueOne(t, db)
	ctx := context.Background()

	require.NoError(t, ns.RecordFailure(ctx, "n1", "连接被拒"))

	got, err := ns.ListPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 1, got[0].Attempts)
	require.Equal(t, "连接被拒", *got[0].LastError)
}

func TestNotificationStoreMarkFailedStopsRetrying(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	_, ns := enqueueOne(t, db)
	ctx := context.Background()

	require.NoError(t, ns.MarkFailed(ctx, "n1", "重试耗尽"))

	got, err := ns.ListPending(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, got, "标记为 failed 后不再重试，留待人工查看")
}

func TestNotificationStoreListPendingRespectsLimit(t *testing.T) {
	db := testDB(t)
	cleanTables(t, db)
	_, ns := enqueueOne(t, db)
	ctx := context.Background()
	require.NoError(t, ns.Enqueue(ctx, &Notification{
		ID: "n2", RequestID: "r1", Event: NotifyEventSubmitted, Channel: "email",
		Recipient: "other@x.com", Subject: "s", Body: "b",
	}))

	got, err := ns.ListPending(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1, "批量上限必须生效，否则积压时一轮会拉爆内存")
}

func TestNotificationsCascadeWithRequest(t *testing.T) {
	// notifications 的外键是 ON DELETE CASCADE：申请单删了通知也跟着走，
	// 不留孤儿行。
	db := testDB(t)
	cleanTables(t, db)
	enqueueOne(t, db)

	_, err := db.Exec(`DELETE FROM requests WHERE id='r1'`)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM notifications`).Scan(&n))
	require.Zero(t, n)
}
