package control

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
)

// approvalFixture 造一套完整依赖：gw 是 key holder 节点，
// requester 是普通成员，approver 在 gw 上持有 org_admin。
func approvalFixture(t *testing.T) (
	svc *ApprovalService, db *sql.DB, sender *fakeSender,
	requesterID, approverID, keyID string,
) {
	t.Helper()
	ctx := context.Background()
	db = testDB(t)
	cleanTables(t, db)

	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path, is_key_holder) VALUES ('gw','网关组','/gw',true)`)
	require.NoError(t, err)
	requesterID = seedUserID(t, db, "requester")
	approverID = seedUserID(t, db, "approver")
	keyID = "k-fixture"
	_, err = db.Exec(`
		INSERT INTO api_keys (id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status, max_budget)
		VALUES ($1,'h-fixture','ak-xxx','gw',$2,'enc','active',5)`, keyID, requesterID)
	require.NoError(t, err)

	orgs := newFakeOrgStore()
	require.NoError(t, orgs.Create(ctx, &Org{ID: "gw", Name: "网关组", IsKeyHolder: true}))

	rbac := newFakeRBACStore()
	rbac.setPath("gw", "/gw")
	require.NoError(t, rbac.CreateGrant(ctx, RoleGrant{
		ID: "g-approver", UserID: approverID, RoleID: authz.RoleOrgAdmin, OrgID: strp("gw"),
	}))

	users := NewPostgresUserStore(db)
	sender = newFakeSender()

	svc = NewApprovalService(ApprovalDeps{
		Requests: NewPostgresRequestStore(db),
		Notifs:   NewPostgresNotificationStore(db),
		Keys:     NewPostgresKeyStore(db),
		Orgs:     orgs,
		RBAC:     rbac,
		Users:    users,
		Resolver: authz.NewResolver(rbac),
	})
	return svc, db, sender, requesterID, approverID, keyID
}

func TestSubmitCreatesPendingRequest(t *testing.T) {
	svc, db, _, requesterID, _, _ := approvalFixture(t)
	ctx := context.Background()

	r, err := svc.Submit(ctx, SubmitInput{
		Kind: RequestKindNewKey, RequesterID: requesterID, OrgID: "gw",
		Reason: "要一把密钥", KeyName: "我的密钥", Models: []string{"qwen-plus"},
	})
	require.NoError(t, err)
	require.Equal(t, RequestStatusPending, r.Status)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM requests WHERE id=$1`, r.ID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestSubmitEnqueuesNotificationToApprovers(t *testing.T) {
	// 在 P1.4 控制台出来之前，这封邮件是审批人得知有待审申请的唯一途径。
	svc, db, _, requesterID, _, _ := approvalFixture(t)
	ctx := context.Background()

	r, err := svc.Submit(ctx, SubmitInput{
		Kind: RequestKindNewKey, RequesterID: requesterID, OrgID: "gw",
		Reason: "要一把密钥", KeyName: "我的密钥", Models: []string{},
	})
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM notifications WHERE request_id=$1 AND event='submitted'`,
		r.ID).Scan(&count))
	require.Equal(t, 1, count, "应给唯一的审批人排一封通知")

	var recipient, status string
	require.NoError(t, db.QueryRow(
		`SELECT recipient, status FROM notifications WHERE request_id=$1`, r.ID).
		Scan(&recipient, &status))
	require.Equal(t, "approver@x.com", recipient)
	require.Equal(t, "pending", status, "只入队，不在提交路径上同步发送")
}

func TestSubmitRejectsUnknownOrg(t *testing.T) {
	svc, _, _, requesterID, _, _ := approvalFixture(t)

	_, err := svc.Submit(context.Background(), SubmitInput{
		Kind: RequestKindNewKey, RequesterID: requesterID, OrgID: "nope",
		Reason: "x", KeyName: "y", Models: []string{},
	})
	require.ErrorIs(t, err, ErrOrgNotFound,
		"目标节点不存在必须在提交时就拒绝，而不是等审批后执行才发现")
}

func TestSubmitQuotaBumpRequiresExistingKey(t *testing.T) {
	svc, _, _, requesterID, _, _ := approvalFixture(t)
	exp := time.Now().Add(24 * time.Hour)

	_, err := svc.Submit(context.Background(), SubmitInput{
		Kind: RequestKindQuotaBump, RequesterID: requesterID, OrgID: "gw",
		Reason: "活动", TargetKeyID: "no-such-key", BumpToBudget: 50, BumpExpiresAt: &exp,
	})
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}

func TestSubmitQuotaBumpSucceeds(t *testing.T) {
	svc, _, _, requesterID, _, keyID := approvalFixture(t)
	exp := time.Now().Add(24 * time.Hour)

	r, err := svc.Submit(context.Background(), SubmitInput{
		Kind: RequestKindQuotaBump, RequesterID: requesterID, OrgID: "gw",
		Reason: "活动", TargetKeyID: keyID, BumpToBudget: 50, BumpExpiresAt: &exp,
	})
	require.NoError(t, err)
	require.Equal(t, RequestKindQuotaBump, r.Kind)
	require.Equal(t, 50.0, *r.BumpToBudget)
}

func TestSubmitRejectsUnknownKind(t *testing.T) {
	svc, _, _, requesterID, _, _ := approvalFixture(t)

	_, err := svc.Submit(context.Background(), SubmitInput{
		Kind: "something_else", RequesterID: requesterID, OrgID: "gw", Reason: "x",
	})
	require.Error(t, err)
}

func submitNewKey(t *testing.T, svc *ApprovalService, requesterID string) *Request {
	t.Helper()
	r, err := svc.Submit(context.Background(), SubmitInput{
		Kind: RequestKindNewKey, RequesterID: requesterID, OrgID: "gw",
		Reason: "要一把密钥", KeyName: "我的密钥", Models: []string{},
	})
	require.NoError(t, err)
	return r
}

func TestApproveMovesToApprovedAndNotifiesRequester(t *testing.T) {
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)

	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	got, err := svc.deps.Requests.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusApproved, got.Status)
	require.Equal(t, approverID, *got.DecidedBy)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM notifications WHERE request_id=$1 AND event='approved'`,
		r.ID).Scan(&n))
	require.Equal(t, 1, n, "申请人要收到批准通知")
}

func TestRejectMovesToRejectedAndNotifies(t *testing.T) {
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)

	require.NoError(t, svc.Reject(ctx, r.ID, approverID))

	got, err := svc.deps.Requests.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusRejected, got.Status)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM notifications WHERE request_id=$1 AND event='rejected'`,
		r.ID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestApproveRequiresKeyWriteOnTheNode(t *testing.T) {
	// 判定下沉到这里做——路径里只有 request ID，中间件拿不到它归属的节点。
	svc, _, _, requesterID, _, _ := approvalFixture(t)
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)

	// 申请人自己没有 key:write，不能批准自己的单子。
	err := svc.Approve(ctx, r.ID, requesterID)
	require.ErrorIs(t, err, ErrPermissionDenied)
}

func TestApproveTwiceIsRejected(t *testing.T) {
	svc, _, _, requesterID, approverID, _ := approvalFixture(t)
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)

	require.NoError(t, svc.Approve(ctx, r.ID, approverID))
	err := svc.Approve(ctx, r.ID, approverID)
	require.ErrorIs(t, err, ErrRequestNotPending)
}

func TestApproveUnknownRequest(t *testing.T) {
	svc, _, _, _, approverID, _ := approvalFixture(t)
	err := svc.Approve(context.Background(), "nope", approverID)
	require.ErrorIs(t, err, ErrRequestNotFound)
}

// withIssuer 给服务装上真实的签发器（Task 10 起才需要）。
func withIssuer(t *testing.T, svc *ApprovalService, db *sql.DB, admin *fakeKeyAdmin) {
	t.Helper()
	orgs := newFakeOrgStore()
	require.NoError(t, orgs.Create(context.Background(),
		&Org{ID: "gw", Name: "网关组", IsKeyHolder: true}))
	svc.deps.Issuer = NewKeyIssuer(KeyIssuerDeps{
		Keys: NewPostgresKeyStore(db), Orgs: orgs,
		Admin: admin, Cipher: testCipher(t),
	})
}

func TestClaimIssuesKeyAndReturnsPlaintextOnce(t *testing.T) {
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	plaintext, k, err := svc.Claim(ctx, r.ID, requesterID)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(plaintext, "ak-"),
		"明文在这次调用里返回，且只此一次")
	require.Equal(t, "active", k.Status)

	got, err := svc.deps.Requests.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusExecuted, got.Status)
	require.Equal(t, k.ID, *got.IssuedKeyID)
}

func TestClaimOnlyByRequester(t *testing.T) {
	// 审批人也不能替申请人领——明文必须只到申请人手里。
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	_, _, err := svc.Claim(ctx, r.ID, approverID)
	require.ErrorIs(t, err, ErrNotRequester)
}

func TestClaimRequiresApproved(t *testing.T) {
	svc, db, _, requesterID, _, _ := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)

	_, _, err := svc.Claim(ctx, r.ID, requesterID)
	require.ErrorIs(t, err, ErrRequestNotApproved, "还没批准就不能领")
}

func TestClaimTwiceIsRejected(t *testing.T) {
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	_, _, err := svc.Claim(ctx, r.ID, requesterID)
	require.NoError(t, err)

	_, _, err = svc.Claim(ctx, r.ID, requesterID)
	require.ErrorIs(t, err, ErrRequestNotApproved,
		"一张单子只能领一次，否则一次审批能换无限把密钥")
}

func TestClaimUpstreamFailureLeavesRequestApproved(t *testing.T) {
	// 上游挂了就返回错误，申请单停在 approved 等重试——
	// 绝不能转 executed，否则申请人再也领不到这把密钥了。
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	admin := newFakeKeyAdmin()
	admin.generateErr = errUpstreamDown
	withIssuer(t, svc, db, admin)
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	_, _, err := svc.Claim(ctx, r.ID, requesterID)
	require.Error(t, err)

	got, err := svc.deps.Requests.Get(ctx, r.ID)
	require.NoError(t, err)
	require.Equal(t, RequestStatusApproved, got.Status, "必须停在 approved 以便重试")
}

func TestConcurrentClaimsYieldExactlyOneKey(t *testing.T) {
	// 顺序调用两次会被状态检查挡住，但并发的两次会同时看到 approved。
	// 真正的守卫在 MarkExecuted 的 status='approved' 条件上；
	// 输掉的那一路必须把自己刚签发的密钥吊销掉。
	svc, db, _, requesterID, approverID, _ := approvalFixture(t)
	withIssuer(t, svc, db, newFakeKeyAdmin())
	ctx := context.Background()
	r := submitNewKey(t, svc, requesterID)
	require.NoError(t, svc.Approve(ctx, r.ID, approverID))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, errs[i] = svc.Claim(ctx, r.ID, requesterID)
		}(i)
	}
	close(start)
	wg.Wait()

	okCount := 0
	for _, err := range errs {
		if err == nil {
			okCount++
		}
	}
	require.Equal(t, 1, okCount, "恰好一次领取成功")

	// 排除 fixture 自带的那把，只数这次领取签出来的。
	var active int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM api_keys
		 WHERE user_id=$1 AND status='active' AND id <> 'k-fixture'`,
		requesterID).Scan(&active))
	require.Equal(t, 1, active, "输掉的那一路必须已把自己签发的密钥吊销")
}
