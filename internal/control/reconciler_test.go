package control

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/cryptobox"
)

// fakeIdentitySource 是可控的 IdP 用户状态来源。
type fakeIdentitySource struct {
	mu      sync.Mutex
	active  map[string]bool // external_id -> 是否在 IdP 侧启用
	err     error
	callCnt int
}

func (f *fakeIdentitySource) ActiveExternalIDs(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCnt++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]bool, len(f.active))
	for k, v := range f.active {
		out[k] = v
	}
	return out, nil
}

// fakeKeyRevoker 记录被吊销的用户。
type fakeKeyRevoker struct {
	mu      sync.Mutex
	revoked []string
	err     error
}

func (f *fakeKeyRevoker) RevokeByUsers(_ context.Context, userIDs []string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.revoked = append(f.revoked, userIDs...)
	return int64(len(userIDs)), nil
}

func newReconcilerFixture(t *testing.T) (*Reconciler, *fakeUserStore, *fakeSessionStore, *fakeIdentitySource, *fakeKeyRevoker) {
	t.Helper()
	users := newFakeUserStore()
	sessions := newFakeSessionStore()
	idp := &fakeIdentitySource{active: map[string]bool{}}
	revoker := &fakeKeyRevoker{}

	r := NewReconciler(ReconcilerDeps{
		Users:    users,
		Sessions: sessions,
		Identity: idp,
		Keys:     revoker,
	})
	return r, users, sessions, idp, revoker
}

func TestReconcileDisablesUserGoneFromIdP(t *testing.T) {
	r, users, sessions, idp, revoker := newReconcilerFixture(t)
	ctx := context.Background()

	stay, err := users.Upsert(ctx, &User{ExternalID: "keep", Email: "k@x", Status: UserStatusActive})
	require.NoError(t, err)
	gone, err := users.Upsert(ctx, &User{ExternalID: "gone", Email: "g@x", Status: UserStatusActive})
	require.NoError(t, err)

	require.NoError(t, sessions.Create(ctx, Session{
		ID: "s-gone", UserID: gone.ID,
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))
	require.NoError(t, sessions.Create(ctx, Session{
		ID: "s-keep", UserID: stay.ID,
		ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now(),
	}))

	idp.active = map[string]bool{"keep": true} // gone 从 IdP 消失了

	res, err := r.ReconcileOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Disabled)

	// 用户被禁用
	g, err := users.ByExternalID(ctx, "gone")
	require.NoError(t, err)
	require.Equal(t, UserStatusDisabled, g.Status)

	// 名下 Key 被吊销
	require.Equal(t, []string{gone.ID}, revoker.revoked)

	// 会话被清掉，但没被禁用的人不受影响
	_, err = sessions.Get(ctx, "s-gone")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = sessions.Get(ctx, "s-keep")
	require.NoError(t, err)
}

func TestReconcileDisablesUserMarkedInactiveInIdP(t *testing.T) {
	r, users, _, idp, revoker := newReconcilerFixture(t)
	ctx := context.Background()

	u, err := users.Upsert(ctx, &User{ExternalID: "u1", Email: "u@x", Status: UserStatusActive})
	require.NoError(t, err)

	idp.active = map[string]bool{"u1": false} // 存在但被禁用

	res, err := r.ReconcileOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Disabled)
	require.Equal(t, []string{u.ID}, revoker.revoked)
}

func TestReconcileNoopWhenAllActive(t *testing.T) {
	r, users, _, idp, revoker := newReconcilerFixture(t)
	ctx := context.Background()

	_, err := users.Upsert(ctx, &User{ExternalID: "u1", Email: "u@x", Status: UserStatusActive})
	require.NoError(t, err)
	idp.active = map[string]bool{"u1": true}

	res, err := r.ReconcileOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, res.Disabled)
	require.Empty(t, revoker.revoked)
}

func TestReconcileAbortsOnIdPFailureWithoutDisablingAnyone(t *testing.T) {
	r, users, _, idp, revoker := newReconcilerFixture(t)
	ctx := context.Background()

	_, err := users.Upsert(ctx, &User{ExternalID: "u1", Email: "u@x", Status: UserStatusActive})
	require.NoError(t, err)
	idp.err = errors.New("IdP 不可达")

	_, err = r.ReconcileOnce(ctx)
	require.Error(t, err)

	u, err := users.ByExternalID(ctx, "u1")
	require.NoError(t, err)
	require.Equal(t, UserStatusActive, u.Status,
		"拉取失败时绝不能把所有人当成已离职——那会造成全员断线")
	require.Empty(t, revoker.revoked)
}

func TestReconcileAbortsOnEmptyIdPResult(t *testing.T) {
	r, users, _, idp, revoker := newReconcilerFixture(t)
	ctx := context.Background()

	_, err := users.Upsert(ctx, &User{ExternalID: "u1", Email: "u@x", Status: UserStatusActive})
	require.NoError(t, err)
	idp.active = map[string]bool{} // 返回空集合

	_, err = r.ReconcileOnce(ctx)
	require.Error(t, err)

	u, err := users.ByExternalID(ctx, "u1")
	require.NoError(t, err)
	require.Equal(t, UserStatusActive, u.Status,
		"IdP 返回空集合更可能是故障而非全员离职，必须当作异常")
	require.Empty(t, revoker.revoked)
}

func TestReconcileRunStopsOnContextCancel(t *testing.T) {
	r, users, _, idp, _ := newReconcilerFixture(t)
	_, err := users.Upsert(context.Background(), &User{
		ExternalID: "u1", Email: "u@x", Status: UserStatusActive,
	})
	require.NoError(t, err)
	idp.active = map[string]bool{"u1": true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 没有响应 context 取消")
	}

	idp.mu.Lock()
	defer idp.mu.Unlock()
	require.Positive(t, idp.callCnt, "至少应该跑过一轮对账")
}

// seedRevokableKey 造一个组织 + 用户 + 一把带真密文的密钥。
func seedRevokableKey(
	t *testing.T, db *sql.DB, cipher *cryptobox.Cipher, upstream, status string,
) string {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO organizations (id, name, path) VALUES ('gw','网关组','/gw')`)
	require.NoError(t, err)
	uid := seedUserID(t, db, "leaver")

	enc, err := cipher.Encrypt(upstream)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO api_keys (id, key_hash, key_prefix, org_id, user_id, upstream_key_enc, status)
		VALUES ('k1','h1','ak-xxx','gw',$1,$2,$3)`, uid, enc, status)
	require.NoError(t, err)
	return uid
}

func TestKeyRevokerBlocksUpstreamToo(t *testing.T) {
	// 手工吊销是本地 + 上游两层，离职对账也该一样——
	// 否则离职场景少了凭据泄漏时的那层兜底。
	db := testDB(t)
	cleanTables(t, db)
	cipher := testCipher(t)
	uid := seedRevokableKey(t, db, cipher, "sk-airlock-leaver", "active")

	admin := newFakeKeyAdmin()
	r := NewPostgresKeyRevoker(db, admin, cipher)

	n, err := r.RevokeByUsers(context.Background(), []string{uid})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	require.Contains(t, admin.callsSnapshot(), "block:sk-airlock-leaver", "上游也要封禁")
}

func TestKeyRevokerAlsoCoversPendingKeys(t *testing.T) {
	// 离职用户名下正在签发中的密钥，上游可能已经建成，必须一并吊销。
	db := testDB(t)
	cleanTables(t, db)
	cipher := testCipher(t)
	uid := seedRevokableKey(t, db, cipher, "sk-airlock-pending", "pending")

	r := NewPostgresKeyRevoker(db, newFakeKeyAdmin(), cipher)

	n, err := r.RevokeByUsers(context.Background(), []string{uid})
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "pending 的密钥也要吊销")

	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM api_keys WHERE id='k1'`).Scan(&status))
	require.Equal(t, "revoked", status)
}

func TestKeyRevokerSucceedsWhenUpstreamBlockFails(t *testing.T) {
	// 上游 block 是第二道防线。它失败不该让离职对账整体失败——
	// 本地已标记 revoked，Edge 下一个请求就会拒绝。
	db := testDB(t)
	cleanTables(t, db)
	cipher := testCipher(t)
	uid := seedRevokableKey(t, db, cipher, "sk-airlock-leaver", "active")

	admin := newFakeKeyAdmin()
	admin.blockErr = errUpstreamDown
	r := NewPostgresKeyRevoker(db, admin, cipher)

	n, err := r.RevokeByUsers(context.Background(), []string{uid})
	require.NoError(t, err, "上游失败不影响吊销生效")
	require.Equal(t, int64(1), n)
}
