package control

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/authz"
	"github.com/softmatrix/airlock/internal/usage"
)

func TestParseRangeDefaultsToLastSevenDays(t *testing.T) {
	from, to, err := parseRange(url.Values{})
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), to, time.Minute)
	require.WithinDuration(t, time.Now().AddDate(0, 0, -7), from, time.Minute)
}

func TestParseRangeAcceptsExplicitDates(t *testing.T) {
	from, to, err := parseRange(url.Values{
		"from": {"2026-09-01"}, "to": {"2026-09-08"},
	})
	require.NoError(t, err)
	require.Equal(t, 2026, from.Year())
	require.Equal(t, time.September, from.Month())
	require.Equal(t, 1, from.Day())
	require.Equal(t, 8, to.Day())
}

func TestParseRangeRejectsTooLongSpan(t *testing.T) {
	// ClickHouse 上的无界扫描要付真金白银的代价，闸放在入口。
	_, _, err := parseRange(url.Values{
		"from": {"2026-01-01"}, "to": {"2026-06-01"},
	})
	require.ErrorIs(t, err, errRangeTooLong)
}

func TestParseRangeRejectsInvertedRange(t *testing.T) {
	_, _, err := parseRange(url.Values{
		"from": {"2026-09-08"}, "to": {"2026-09-01"},
	})
	require.ErrorIs(t, err, errRangeInverted)
}

func TestParseRangeRejectsUnparsableDate(t *testing.T) {
	_, _, err := parseRange(url.Values{"from": {"上周"}})
	require.ErrorIs(t, err, errRangeInvalid)
}

// fakeUsageReader 回放预置的聚合结果，并记下收到的查询条件——
// 这个测试关心的是「谁能看多少」的判定，不是 SQL 本身
// （SQL 由 internal/usage 对真实 ClickHouse 测）。
type fakeUsageReader struct {
	lastSummary usage.SummaryQuery
	lastAudit   usage.AuditQuery
	summaryRows []usage.SummaryRow
	auditRows   []usage.AuditRow
	err         error
}

func (f *fakeUsageReader) Summary(_ context.Context, q usage.SummaryQuery) ([]usage.SummaryRow, error) {
	f.lastSummary = q
	return f.summaryRows, f.err
}

func (f *fakeUsageReader) Records(_ context.Context, q usage.AuditQuery) ([]usage.AuditRow, error) {
	f.lastAudit = q
	return f.auditRows, f.err
}

// usageFixture 造一棵 root/rd/rd2 的真实组织树（子树解析要查 Postgres）
// 与一个 UsageAPI。
func usageFixture(t *testing.T) (*UsageAPI, *fakeUsageReader, *fakeRBACStore, *sql.DB) {
	t.Helper()
	db := testDB(t)
	cleanTables(t, db)
	seedGrantOrg(t, db, "root", "/root")
	seedGrantOrg(t, db, "rd", "/root/rd")
	seedGrantOrg(t, db, "rd2", "/root/rd2")

	rbac := newFakeRBACStore()
	rbac.setPath("root", "/root")
	rbac.setPath("rd", "/root/rd")
	rbac.setPath("rd2", "/root/rd2")

	reader := &fakeUsageReader{}
	api := NewUsageAPI(reader, NewPostgresOrgStore(db), authz.NewResolver(rbac))
	return api, reader, rbac, db
}

func summaryReq(t *testing.T, api *UsageAPI, u *User, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := asUser(httptest.NewRequest(http.MethodGet, "/api/usage/summary"+query, nil), u)
	rec := httptest.NewRecorder()
	api.HandleSummary(rec, req)
	return rec
}

func TestSummaryGlobalCostReaderSeesEverything(t *testing.T) {
	api, reader, rbac, _ := usageFixture(t)
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-fin", UserID: "fin", RoleID: authz.RoleFinOps,
	}))

	rec := summaryReq(t, api, &User{ID: "fin", Status: UserStatusActive}, "?group_by=org")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, reader.lastSummary.OrgIDs,
		"持全局 cost:read_all 的人不该被收窄范围")
}

func TestSummarySubtreeCostReaderIsScoped(t *testing.T) {
	api, reader, rbac, _ := usageFixture(t)
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-rd", UserID: "boss", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	rec := summaryReq(t, api, &User{ID: "boss", Status: UserStatusActive}, "?group_by=org")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"rd"}, reader.lastSummary.OrgIDs)
}

func TestSummarySpareSiblingWithSamePrefix(t *testing.T) {
	// 第七次遇到这个陷阱（P1.2b 权限判定、P1.3b 审批人查找、P1.3c 子树吊销、
	// P1.4a 待审列表、P1.4b 子树密钥查询、P1.4c 有效授予各踩过一次）：
	// /root/rd 是 /root/rd2 的字符串前缀，但 rd2 不在 rd 的子树里。
	//
	// 这次靠复用 OrgStore.Subtree（它已经用了带分隔符的前缀匹配），
	// 测试是钉住这个性质，不是重新实现它。成本串台意味着一个部门
	// 能看到另一个部门的花销。
	api, reader, rbac, _ := usageFixture(t)
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-rd", UserID: "boss", RoleID: authz.RoleOrgAdmin, OrgID: strp("rd"),
	}))

	rec := summaryReq(t, api, &User{ID: "boss", Status: UserStatusActive}, "?group_by=org")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, reader.lastSummary.OrgIDs, "rd2",
		"同前缀兄弟节点的成本不能进入可见范围")
}

func TestSummaryWithoutAnyCostPermissionIs403(t *testing.T) {
	api, _, _, _ := usageFixture(t)

	rec := summaryReq(t, api, &User{ID: "nobody", Status: UserStatusActive}, "?group_by=org")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "permission_denied")
}

func TestSummaryTooLongRangeIs400(t *testing.T) {
	api, _, rbac, _ := usageFixture(t)
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-fin", UserID: "fin", RoleID: authz.RoleFinOps,
	}))

	rec := summaryReq(t, api, &User{ID: "fin", Status: UserStatusActive},
		"?group_by=org&from=2026-01-01&to=2026-06-01")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "range_too_long")
}

func TestSummaryWithoutReaderIs503(t *testing.T) {
	// CLICKHOUSE_DSN 未配置时控制面照常启动，这个接口回「未启用」。
	db := testDB(t)
	cleanTables(t, db)
	rbac := newFakeRBACStore()
	api := NewUsageAPI(nil, NewPostgresOrgStore(db), authz.NewResolver(rbac))
	require.NoError(t, rbac.CreateGrant(context.Background(), RoleGrant{
		ID: "g-fin", UserID: "fin", RoleID: authz.RoleFinOps,
	}))

	rec := summaryReq(t, api, &User{ID: "fin", Status: UserStatusActive}, "?group_by=org")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "analytics_disabled")
}
