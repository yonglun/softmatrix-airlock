package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/cryptobox"
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
