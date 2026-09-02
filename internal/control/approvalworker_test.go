package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/litellm"
)

func litellmKeyForTest(key string, budget float64) litellm.Key {
	return litellm.Key{Key: key, Models: []string{}, MaxBudget: &budget}
}

// workerFixture 在 approvalFixture 之上装出一个 worker。
func workerFixture(t *testing.T) (
	w *ApprovalWorker, svc *ApprovalService, db *sql.DB,
	admin *fakeKeyAdmin, sender *fakeSender,
	requesterID, approverID, keyID string,
) {
	t.Helper()
	svc, db, sender, requesterID, approverID, keyID = approvalFixture(t)
	admin = newFakeKeyAdmin()
	withIssuer(t, svc, db, admin)

	// fixture 里那把密钥的 upstream_key_enc 是占位字符串，解不开。
	// worker 要真去解密，所以在这里换成真密文，并让上游也认得它。
	cipher := testCipher(t)
	enc, err := cipher.Encrypt("sk-airlock-fixture")
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE api_keys SET upstream_key_enc=$1 WHERE id=$2`, enc, keyID)
	require.NoError(t, err)
	require.NoError(t, admin.GenerateKey(context.Background(),
		litellmKeyForTest("sk-airlock-fixture", 5)))

	w = NewApprovalWorker(ApprovalWorkerDeps{
		Requests: NewPostgresRequestStore(db),
		Notifs:   NewPostgresNotificationStore(db),
		Keys:     NewPostgresKeyStore(db),
		Users:    NewPostgresUserStore(db),
		Admin:    admin,
		Cipher:   cipher,
		Sender:   sender,
	})
	return w, svc, db, admin, sender, requesterID, approverID, keyID
}

func approvedBump(
	t *testing.T, svc *ApprovalService, requesterID, approverID, keyID string, expires time.Time,
) *Request {
	t.Helper()
	ctx := context.Background()
	r, err := svc.Submit(ctx, SubmitInput{
		Kind: RequestKindQuotaBump, RequesterID: requesterID, OrgID: "gw",
		Reason: "活动", TargetKeyID: keyID, BumpToBudget: 50, BumpExpiresAt: &expires,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))
	return r
}

func TestExecuteBumpsRaisesUpstreamBudgetAndRecordsPrev(t *testing.T) {
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	admin.resetCalls()

	n, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := NewPostgresRequestStore(db).Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusExecuted, got.Status)
	require.Equal(t, 5.0, *got.PrevBudget, "必须记下原值，到期照此回收")

	require.Contains(t, admin.callsSnapshot(), "update-budget:50")
}

func TestExecuteBumpsIsIdempotentAcrossRuns(t *testing.T) {
	w, svc, _, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))

	_, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	admin.resetCalls()

	n, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, admin.callsSnapshot(), "已执行的不该再动上游")
}

func TestExecuteBumpsKeepsApprovedOnUpstreamFailure(t *testing.T) {
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	admin.updateErr = errUpstreamDown

	_, err := w.ExecuteApprovedBumps(ctx)
	require.Error(t, err)

	got, err := NewPostgresRequestStore(db).Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusApproved, got.Status, "留在 approved 等下一轮重试")
	require.Equal(t, 1, got.Attempts)
}

func TestExecuteBumpsGivesUpAfterMaxAttempts(t *testing.T) {
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	admin.updateErr = errUpstreamDown

	for i := 0; i < maxExecuteAttempts; i++ {
		_, _ = w.ExecuteApprovedBumps(ctx)
	}

	got, err := NewPostgresRequestStore(db).Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusFailed, got.Status,
		"无限重试会让一张坏单子每轮都刷日志，到点要停下来交给人")
}

func TestReclaimRestoresPreviousBudget(t *testing.T) {
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	_, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)

	// 把到期时间推到过去，模拟已过期。
	_, err = db.ExecContext(ctx,
		`UPDATE requests SET bump_expires_at = now() - interval '1 hour' WHERE id=$1`, r.ID)
	require.NoError(t, err)
	admin.resetCalls()

	n, err := w.ReclaimExpiredBumps(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.Contains(t, admin.callsSnapshot(), "update-budget:5", "恢复成 prev_budget")

	got, err := NewPostgresRequestStore(db).Get(ctx, r.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ReclaimedAt)
}

func TestReclaimIgnoresUnexpiredBumps(t *testing.T) {
	w, svc, _, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	_, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	admin.resetCalls()

	n, err := w.ReclaimExpiredBumps(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, admin.callsSnapshot(), "没到期就绝不能动用户的额度")
}

func TestReclaimIsIdempotent(t *testing.T) {
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	_, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE requests SET bump_expires_at = now() - interval '1 hour' WHERE id=$1`, r.ID)
	require.NoError(t, err)

	_, err = w.ReclaimExpiredBumps(ctx)
	require.NoError(t, err)
	admin.resetCalls()

	n, err := w.ReclaimExpiredBumps(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, admin.callsSnapshot(), "已回收的不该重复回收")
}

func TestReclaimSurvivesRestart(t *testing.T) {
	// 这条盯的是设计文档 D6：回收必须是扫描而不是定时器。
	// 用一个全新的 worker 实例扫描——等价于进程重启后的第一轮，
	// 若实现依赖内存里的定时器，这里必然回收不到。
	w, svc, db, admin, _, requesterID, approverID, keyID := workerFixture(t)
	ctx := context.Background()
	r := approvedBump(t, svc, requesterID, approverID, keyID, time.Now().Add(24*time.Hour))
	_, err := w.ExecuteApprovedBumps(ctx)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`UPDATE requests SET bump_expires_at = now() - interval '1 hour' WHERE id=$1`, r.ID)
	require.NoError(t, err)

	fresh := NewApprovalWorker(ApprovalWorkerDeps{
		Requests: NewPostgresRequestStore(db),
		Notifs:   NewPostgresNotificationStore(db),
		Keys:     NewPostgresKeyStore(db),
		Users:    NewPostgresUserStore(db),
		Admin:    admin, Cipher: testCipher(t), Sender: newFakeSender(),
	})

	n, err := fresh.ReclaimExpiredBumps(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "重启后必须仍能回收，否则临时提额会变成永久")

	got, err := NewPostgresRequestStore(db).Get(ctx, r.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ReclaimedAt)
}

func TestDeliverPendingSendsAndMarksSent(t *testing.T) {
	w, svc, db, _, sender, requesterID, _, _ := workerFixture(t)
	ctx := context.Background()
	submitNewKey(t, svc, requesterID) // 提交会给审批人排一封

	n, err := w.DeliverPending(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []string{"approver@x.com"}, sender.recipients())

	var pending int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM notifications WHERE status='pending'`).Scan(&pending))
	require.Zero(t, pending)
}

func TestDeliverPendingRetriesOnFailure(t *testing.T) {
	// 邮件服务器抖一下不该让通知丢掉——那意味着审批人永远不知道有待审申请。
	w, svc, db, _, sender, requesterID, _, _ := workerFixture(t)
	ctx := context.Background()
	submitNewKey(t, svc, requesterID)
	sender.setErr(errSMTPDown)

	_, err := w.DeliverPending(ctx)
	require.Error(t, err)

	var status string
	var attempts int
	require.NoError(t, db.QueryRow(
		`SELECT status, attempts FROM notifications`).Scan(&status, &attempts))
	require.Equal(t, "pending", status, "仍留在队列里等下一轮")
	require.Equal(t, 1, attempts)

	// 恢复后应当送达。
	sender.setErr(nil)
	n, err := w.DeliverPending(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestDeliverPendingGivesUpAfterMaxAttempts(t *testing.T) {
	w, svc, db, _, sender, requesterID, _, _ := workerFixture(t)
	ctx := context.Background()
	submitNewKey(t, svc, requesterID)
	sender.setErr(errSMTPDown)

	for i := 0; i < maxDeliverAttempts; i++ {
		_, _ = w.DeliverPending(ctx)
	}

	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM notifications`).Scan(&status))
	require.Equal(t, "failed", status)

	got, err := w.DeliverPending(ctx)
	require.NoError(t, err)
	require.Zero(t, got, "标记 failed 后不再重试")
}

func TestNudgeCoalescesApprovalWork(t *testing.T) {
	w, _, _, _, _, _, _, _ := workerFixture(t)

	for i := 0; i < 10; i++ {
		w.Nudge()
	}
	require.Len(t, w.trigger, 1, "多次 Nudge 只积压一轮")
}

func TestNudgeOnNilApprovalWorkerIsNoOp(t *testing.T) {
	var w *ApprovalWorker
	require.NotPanics(t, func() { w.Nudge() })
}

func TestRunDeliversThenExitsOnCancel(t *testing.T) {
	w, svc, _, _, sender, requesterID, _, _ := workerFixture(t)
	submitNewKey(t, svc, requesterID)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx, time.Hour) // ticker 很长，确保这轮是启动时那次
		close(done)
	}()

	require.Eventually(t, func() bool {
		return len(sender.recipients()) == 1
	}, 2*time.Second, 10*time.Millisecond, "启动后应立即跑一轮")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Run 应当退出")
	}
}
