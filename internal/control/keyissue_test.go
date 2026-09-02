package control

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/litellm"
)

func testCipher(t *testing.T) *cryptobox.Cipher {
	t.Helper()
	c, err := cryptobox.NewCipher([]byte("airlock-dev-only-32byte-key!!!!!"))
	require.NoError(t, err)
	return c
}

// issuerFixture 造一个组织树里已有 key holder 节点 gw 的签发器。
//
// 一并返回 *sql.DB：Task 9 的滞留清理测试需要直接改 created_at 造出
// 「卡住的 pending」。userID 是沿路种下的真实用户——api_keys.user_id 上
// 有一条 Task 2 才发现的既有外键 api_keys_user_fk，IssueRequest.UserID
// 若用字面量会在 CreatePending 时被拒。一次定死 6 元组签名。
func issuerFixture(t *testing.T) (iss *KeyIssuer, orgs *fakeOrgStore, admin *fakeKeyAdmin, keys KeyStore, db *sql.DB, userID string) {
	t.Helper()
	db = testDB(t)
	cleanTables(t, db)
	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path, is_key_holder) VALUES ('gw','网关组','/gw',true)`)
	require.NoError(t, err)
	userID = seedUserID(t, db, "issuer-fixture-user")

	orgs = newFakeOrgStore()
	require.NoError(t, orgs.Create(context.Background(),
		&Org{ID: "gw", Name: "网关组", IsKeyHolder: true}))

	keys = NewPostgresKeyStore(db)
	admin = newFakeKeyAdmin()
	iss = NewKeyIssuer(KeyIssuerDeps{
		Keys: keys, Orgs: orgs, Admin: admin, Cipher: testCipher(t),
	})
	return iss, orgs, admin, keys, db, userID
}

func TestIssueReturnsPlaintextOnceAndStoresOnlyHash(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	plain, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试", Models: []string{"qwen-plus"},
	})
	require.NoError(t, err)
	require.True(t, len(plain) > 3 && plain[:3] == "ak-", "返回的是 ak- 明文")

	stored, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, "active", stored.Status)
	require.NotEqual(t, plain, stored.KeyHash, "库里存的是哈希不是明文")
	require.NotContains(t, stored.UpstreamKeyEnc, "sk-", "上游密钥必须加密存储")

	require.Len(t, admin.callsSnapshot(), 1)
}

func TestIssueSendsQuotaAndWhitelistUpstream(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	budget := 10.5
	rpm := 60

	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试",
		Models: []string{"qwen-plus"}, MaxBudget: &budget, RPMLimit: &rpm,
	})
	require.NoError(t, err)

	stored, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	upstream, ok := admin.generated(decryptForTest(t, stored.UpstreamKeyEnc))
	require.True(t, ok, "上游应已建成该密钥")

	require.Equal(t, []string{"qwen-plus"}, upstream.Models)
	require.Equal(t, "gw", *upstream.TeamID, "必须绑定到节点对应的 Team")
	require.Equal(t, 10.5, *upstream.MaxBudget)
	require.Equal(t, 60, *upstream.RPMLimit)
}

func TestIssueConvertsExpiryToRelativeDuration(t *testing.T) {
	// 上游会静默丢弃绝对时间 expires，必须换算成相对时长。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	exp := time.Now().Add(2 * time.Hour)

	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试",
		Models: []string{}, ExpiresAt: &exp,
	})
	require.NoError(t, err)

	stored, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	upstream, ok := admin.generated(decryptForTest(t, stored.UpstreamKeyEnc))
	require.True(t, ok)

	require.NotNil(t, upstream.Duration)
	require.Regexp(t, `^\d+s$`, *upstream.Duration, "必须是秒级相对时长")
}

func TestIssueRejectsNonKeyHolderNode(t *testing.T) {
	iss, orgs, admin, _, _, uid := issuerFixture(t)
	ctx := context.Background()
	require.NoError(t, orgs.Create(ctx, &Org{ID: "plain", Name: "普通部门"}))

	_, _, err := iss.Issue(ctx, IssueRequest{
		OrgID: "plain", UserID: uid, Name: "测试", Models: []string{},
	})
	require.ErrorIs(t, err, ErrOrgNotKeyHolder)
	require.Empty(t, admin.callsSnapshot(), "校验不通过时不该调上游")
}

func TestIssueRejectsUnknownNode(t *testing.T) {
	iss, _, _, _, _, uid := issuerFixture(t)

	_, _, err := iss.Issue(context.Background(), IssueRequest{
		OrgID: "nope", UserID: uid, Name: "测试", Models: []string{},
	})
	require.ErrorIs(t, err, ErrOrgNotFound)
}

func decryptForTest(t *testing.T, enc string) string {
	t.Helper()
	plain, err := testCipher(t).Decrypt(enc)
	require.NoError(t, err)
	return plain
}

func TestIssueUpstreamFailureLeavesNoTrace(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	admin.generateErr = errUpstreamDown

	_, _, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试", Models: []string{},
	})
	require.Error(t, err)

	list, err := keys.ListByOrg(ctx, "gw")
	require.NoError(t, err)
	require.Empty(t, list, "失败后不该留下任何本地行")
}

func TestCleanupDeletesUpstreamBeforeLocalRow(t *testing.T) {
	// 顺序颠倒会正好制造出无主凭据：本地行一删，
	// 上游那把仍然能用的密钥就再也没人知道了。
	iss, _, admin, _, _, _ := issuerFixture(t)
	ctx := context.Background()

	// 让上游「建成功了」但 MarkActive 失败，从而触发 cleanup。
	// 用一个已被删除的 store 制造 MarkActive 失败不现实，
	// 因此直接验证 cleanup 本身的调用顺序。
	upstream := "sk-airlock-forcleanup"
	require.NoError(t, admin.GenerateKey(ctx, litellm.Key{Key: upstream, Models: []string{}}))
	admin.resetCalls()

	iss.cleanup(ctx, "no-such-row", upstream)

	calls := admin.callsSnapshot()
	require.Equal(t, []string{"exists:" + upstream, "delete:" + upstream}, calls,
		"必须先确认存在再删除，且只对上游发这两个调用")
	require.False(t, admin.has(upstream), "上游密钥应已删除")
}

func TestCleanupKeepsLocalRowWhenUpstreamUnreachable(t *testing.T) {
	// 查不清上游状态时宁可留一条看得见的 pending 行，
	// 也不能删掉它并可能留下一把无主凭据。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	k := sampleKey("k-stuck", "h-stuck")
	k.UserID = uid
	require.NoError(t, keys.CreatePending(ctx, k))
	admin.existsErr = errUpstreamDown

	iss.cleanup(ctx, "k-stuck", "sk-airlock-whatever")

	got, err := keys.Get(ctx, "k-stuck")
	require.NoError(t, err, "上游状态不明时必须保留本地行")
	require.Equal(t, "pending", got.Status)
}

func TestCleanupSkipsDeleteWhenUpstreamAbsent(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	k := sampleKey("k-absent", "h-absent")
	k.UserID = uid
	require.NoError(t, keys.CreatePending(ctx, k))
	admin.resetCalls()

	iss.cleanup(ctx, "k-absent", "sk-airlock-never-created")

	for _, c := range admin.callsSnapshot() {
		require.NotContains(t, c, "delete:", "上游本就不存在时不该发删除请求")
	}
	_, err := keys.Get(ctx, "k-absent")
	require.ErrorIs(t, err, ErrAPIKeyNotFound, "本地行仍应被清掉")
}

func TestRevokeMarksLocalFirstThenBlocksUpstream(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试", Models: []string{},
	})
	require.NoError(t, err)
	admin.resetCalls()

	require.NoError(t, iss.Revoke(ctx, k.ID))

	stored, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", stored.Status, "本地状态是主闸门，必须先改")

	calls := admin.callsSnapshot()
	require.Len(t, calls, 1)
	require.Contains(t, calls[0], "block:", "上游用 block 而非 delete，保住审计关联")
}

func TestRevokeSucceedsEvenIfUpstreamBlockFails(t *testing.T) {
	// 上游 block 是第二道防线。它失败不该让吊销整体失败——
	// 本地已标记 revoked，Edge 下一个请求就会拒绝该密钥。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "测试", Models: []string{},
	})
	require.NoError(t, err)
	admin.blockErr = errUpstreamDown

	require.NoError(t, iss.Revoke(ctx, k.ID), "上游失败不影响吊销生效")

	stored, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", stored.Status)
}

func TestRevokeUnknownKey(t *testing.T) {
	iss, _, _, _, _, _ := issuerFixture(t)
	err := iss.Revoke(context.Background(), "nope")
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}

func TestCleanupStalePendingRemovesStuckRows(t *testing.T) {
	iss, _, admin, keys, db, uid := issuerFixture(t)
	ctx := context.Background()

	// 模拟「上游建成了、但进程在 MarkActive 之前崩了」。
	k := sampleKey("k-crash", "h-crash")
	k.UserID = uid
	upstream := "sk-airlock-crashcase"
	enc, err := testCipher(t).Encrypt(upstream)
	require.NoError(t, err)
	k.UpstreamKeyEnc = enc
	require.NoError(t, keys.CreatePending(ctx, k))
	require.NoError(t, admin.GenerateKey(ctx, litellm.Key{Key: upstream, Models: []string{}}))

	_, err = db.ExecContext(ctx,
		`UPDATE api_keys SET created_at = now() - interval '1 hour' WHERE id='k-crash'`)
	require.NoError(t, err)

	n, err := iss.CleanupStalePending(ctx, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.False(t, admin.has(upstream), "上游残骸应被删除")
	_, err = keys.Get(ctx, "k-crash")
	require.ErrorIs(t, err, ErrAPIKeyNotFound, "本地行应被清掉")
}

func TestCleanupStalePendingIgnoresFreshRows(t *testing.T) {
	iss, _, _, keys, _, uid := issuerFixture(t)
	ctx := context.Background()

	k := sampleKey("k-fresh", "h-fresh")
	k.UserID = uid
	require.NoError(t, keys.CreatePending(ctx, k))

	n, err := iss.CleanupStalePending(ctx, 10*time.Minute)
	require.NoError(t, err)
	require.Zero(t, n, "刚创建的 pending 可能正在签发中，绝不能碰")

	_, err = keys.Get(ctx, "k-fresh")
	require.NoError(t, err)
}

func TestRotateReturnsNewPlaintextAndKeepsUpstreamKey(t *testing.T) {
	// D1/D2 的核心：轮换只换客户端凭据，上游密钥原封不动——
	// 预算桶因此连续，不会在窗口期裂成两份。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	oldPlain, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "轮换测试", Models: []string{},
	})
	require.NoError(t, err)
	before, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	admin.resetCalls()

	newPlain, rotated, err := iss.Rotate(ctx, k.ID, time.Hour)
	require.NoError(t, err)

	require.NotEqual(t, oldPlain, newPlain, "必须换出一把新的客户端凭据")
	require.True(t, strings.HasPrefix(newPlain, "ak-"))
	require.Equal(t, k.ID, rotated.ID, "密钥身份跨轮换稳定")
	require.Equal(t, before.UpstreamKeyEnc, rotated.UpstreamKeyEnc,
		"上游密钥必须原封不动——预算桶靠它保持连续")
	require.Empty(t, admin.callsSnapshot(), "轮换不调用上游")
}

func TestRotateStoresOnlyHashOfNewPlaintext(t *testing.T) {
	iss, _, _, keys, db, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)

	newPlain, _, err := iss.Rotate(ctx, k.ID, time.Hour)
	require.NoError(t, err)

	var stored string
	require.NoError(t, db.QueryRow(
		`SELECT key_hash FROM api_keys WHERE id=$1`, k.ID).Scan(&stored))
	require.NotEqual(t, newPlain, stored, "库里绝不能出现明文")
	require.Equal(t, apikey.Hash(newPlain), stored)

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PrevKeyHash)
}

func TestRotateRejectsRevokedKey(t *testing.T) {
	iss, _, _, _, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, iss.Revoke(ctx, k.ID))

	_, _, err = iss.Rotate(ctx, k.ID, time.Hour)
	require.ErrorIs(t, err, ErrKeyNotActive)
}

func TestRotateDefaultsAndCapsWindow(t *testing.T) {
	iss, _, _, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)

	// 0 表示用默认值。
	_, _, err = iss.Rotate(ctx, k.ID, 0)
	require.NoError(t, err)
	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(defaultRotationWindow),
		*got.PrevKeyExpiresAt, time.Minute)

	// 超过上限直接拒绝——否则填个 10 年就把轮换变成了摆设。
	_, _, err = iss.Rotate(ctx, k.ID, maxRotationWindow+time.Hour)
	require.ErrorIs(t, err, ErrWindowTooLong)
}

func TestRotateTwiceKeepsOnlyOneGenerationOfGrace(t *testing.T) {
	// 行里只有一个 prev 位置：窗口内二次轮换会让最初那把当场失效。
	// 这是刻意的——连轮两次通常意味着第一次轮出来的也泄漏了。
	iss, _, _, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	firstPlain, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)

	secondPlain, _, err := iss.Rotate(ctx, k.ID, time.Hour)
	require.NoError(t, err)
	_, _, err = iss.Rotate(ctx, k.ID, time.Hour)
	require.NoError(t, err)

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, apikey.Hash(secondPlain), *got.PrevKeyHash,
		"prev 位置上应是上一次轮出来的那把")
	require.NotEqual(t, apikey.Hash(firstPlain), *got.PrevKeyHash,
		"最初那把必须当场失效")
}

func TestBlockPendingUpstreamBlocksAndStamps(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	// 造出「本地已吊销、上游没封成」的状态：直接改状态，绕开 Revoke。
	require.NoError(t, keys.Revoke(ctx, k.ID))
	admin.resetCalls()

	n, err := iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UpstreamBlockedAt, "封成了就要盖戳")
	require.Len(t, admin.callsSnapshot(), 1)
}

func TestBlockPendingUpstreamIsIdempotentAcrossRuns(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(ctx, k.ID))

	_, err = iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	admin.resetCalls()

	n, err := iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, admin.callsSnapshot(), "盖过戳的不该再动上游")
}

func TestBlockPendingUpstreamTreats404AsDone(t *testing.T) {
	// 探针 P1：上游对不存在的 key 返回 404。那把密钥本来就不在了，
	// 当作封成功盖戳，否则它会一直占着重试额度。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(ctx, k.ID))
	admin.blockNotFound = true

	n, err := iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UpstreamBlockedAt)
}

func TestBlockPendingUpstreamRetriesOnFailure(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(ctx, k.ID))
	admin.blockErr = errUpstreamDown

	_, err = iss.BlockPendingUpstream(ctx)
	require.Error(t, err)

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Nil(t, got.UpstreamBlockedAt, "没封成就不能盖戳")
	require.Equal(t, 1, got.UpstreamBlockAttempts)

	// 上游恢复后下一轮应当收敛。
	admin.blockErr = nil
	n, err := iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestBlockPendingUpstreamGivesUpAfterMaxAttempts(t *testing.T) {
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(ctx, k.ID))
	admin.blockErr = errUpstreamDown

	for i := 0; i < maxBlockAttempts; i++ {
		_, _ = iss.BlockPendingUpstream(ctx)
	}
	admin.resetCalls()

	n, err := iss.BlockPendingUpstream(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, admin.callsSnapshot(),
		"重试耗尽后就停下来交给人，不让一行坏数据每轮刷日志")
}

func TestRevokeStampsUpstreamBlockedOnSuccess(t *testing.T) {
	// 单把吊销走内联封禁：成功即盖戳，扫描下一轮就不用再管它。
	iss, _, _, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)

	require.NoError(t, iss.Revoke(ctx, k.ID))

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UpstreamBlockedAt)
}

func TestRevokeLeavesStampEmptyWhenUpstreamFails(t *testing.T) {
	// 上游失败不该让吊销失败（既有语义），但必须留白让扫描重试——
	// 这正是本阶段补上的那个缺口。
	iss, _, admin, keys, _, uid := issuerFixture(t)
	ctx := context.Background()
	_, k, err := iss.Issue(ctx, IssueRequest{
		OrgID: "gw", UserID: uid, Name: "x", Models: []string{},
	})
	require.NoError(t, err)
	admin.blockErr = errUpstreamDown

	require.NoError(t, iss.Revoke(ctx, k.ID), "上游失败不影响吊销生效")

	got, err := keys.Get(ctx, k.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", got.Status)
	require.Nil(t, got.UpstreamBlockedAt, "没封成就留白，交给扫描")
}
