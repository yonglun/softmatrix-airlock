package usage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSummaryGroupsByEachDimension(t *testing.T) {
	conn := testCH(t)
	cleanUsage(t, conn)
	r := &Reader{conn: conn}
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedUsage(t, conn, usageAt(base, "rd", "alice", "qwen-plus", 100))
	seedUsage(t, conn, usageAt(base.Add(time.Hour), "rd", "bob", "qwen-plus", 200))
	seedUsage(t, conn, usageAt(base.Add(48*time.Hour), "sales", "alice", "qwen-max", 400))

	from, to := base.Add(-time.Hour), base.Add(72*time.Hour)

	byOrg, err := r.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: GroupByOrg})
	require.NoError(t, err)
	require.Len(t, byOrg, 2)
	got := map[string]int64{}
	for _, row := range byOrg {
		got[row.Key] = row.CostMicro
	}
	require.Equal(t, int64(300), got["rd"], "两条 rd 记录的成本要加起来")
	require.Equal(t, int64(400), got["sales"])

	byUser, err := r.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: GroupByUser})
	require.NoError(t, err)
	require.Len(t, byUser, 2, "alice 跨两个组织的记录要合并成一行")

	byModel, err := r.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: GroupByModel})
	require.NoError(t, err)
	require.Len(t, byModel, 2)

	byDay, err := r.Summary(ctx, SummaryQuery{From: from, To: to, GroupBy: GroupByDay})
	require.NoError(t, err)
	require.Len(t, byDay, 2, "9/1 与 9/3 各一天")
	require.Equal(t, "2026-09-01", byDay[0].Key, "按天要按时间正序")
}

func TestSummaryRespectsTimeRange(t *testing.T) {
	conn := testCH(t)
	cleanUsage(t, conn)
	r := &Reader{conn: conn}
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedUsage(t, conn, usageAt(base, "rd", "alice", "qwen-plus", 100))
	seedUsage(t, conn, usageAt(base.Add(240*time.Hour), "rd", "alice", "qwen-plus", 999))

	rows, err := r.Summary(ctx, SummaryQuery{
		From: base.Add(-time.Hour), To: base.Add(time.Hour), GroupBy: GroupByOrg,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(100), rows[0].CostMicro, "范围外的记录不能算进来")
}

func TestSummaryFiltersByOrgIDs(t *testing.T) {
	conn := testCH(t)
	cleanUsage(t, conn)
	r := &Reader{conn: conn}
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedUsage(t, conn, usageAt(base, "rd", "alice", "qwen-plus", 100))
	seedUsage(t, conn, usageAt(base, "rd2", "bob", "qwen-plus", 500))

	rows, err := r.Summary(ctx, SummaryQuery{
		From: base.Add(-time.Hour), To: base.Add(time.Hour),
		GroupBy: GroupByOrg, OrgIDs: []string{"rd"},
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "rd", rows[0].Key, "只圈定的组织能出现")
	require.Equal(t, int64(100), rows[0].CostMicro)
}

func TestSummaryRejectsUnknownDimension(t *testing.T) {
	// group_by 是用户可控字符串，必须走白名单——绝不能拼进 SQL。
	conn := testCH(t)
	r := &Reader{conn: conn}

	_, err := r.Summary(context.Background(), SummaryQuery{
		From: time.Now().Add(-time.Hour), To: time.Now(), GroupBy: "org_id; DROP TABLE",
	})
	require.ErrorIs(t, err, ErrUnknownGroupBy)
}

func TestRecordsFiltersAndPages(t *testing.T) {
	conn := testCH(t)
	cleanUsage(t, conn)
	r := &Reader{conn: conn}
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedUsage(t, conn, usageAt(base, "rd", "alice", "qwen-plus", 100))
	seedUsage(t, conn, usageAt(base.Add(time.Minute), "rd", "bob", "qwen-max", 200))
	seedUsage(t, conn, usageAt(base.Add(2*time.Minute), "sales", "alice", "qwen-plus", 300))

	from, to := base.Add(-time.Hour), base.Add(time.Hour)

	all, err := r.Records(ctx, AuditQuery{From: from, To: to, Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.True(t, all[0].Timestamp.After(all[1].Timestamp), "默认按时间倒序，最新的在前")

	byOrg, err := r.Records(ctx, AuditQuery{From: from, To: to, OrgID: "rd", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byOrg, 2)

	byUser, err := r.Records(ctx, AuditQuery{From: from, To: to, UserID: "alice", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byUser, 2)

	byModel, err := r.Records(ctx, AuditQuery{From: from, To: to, Model: "qwen-max", Limit: 10})
	require.NoError(t, err)
	require.Len(t, byModel, 1)

	page1, err := r.Records(ctx, AuditQuery{From: from, To: to, Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	page2, err := r.Records(ctx, AuditQuery{From: from, To: to, Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.NotEqual(t, page1[0].RequestID, page2[0].RequestID, "翻页不能重复给同一条")
}

func TestRecordsOnlyErrors(t *testing.T) {
	conn := testCH(t)
	cleanUsage(t, conn)
	r := &Reader{conn: conn}
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	ok := usageAt(base, "rd", "alice", "qwen-plus", 100)
	bad := usageAt(base.Add(time.Minute), "rd", "bob", "qwen-plus", 0)
	bad.StatusCode = 429
	bad.ErrorType = "rate_limited"
	seedUsage(t, conn, ok)
	seedUsage(t, conn, bad)

	rows, err := r.Records(ctx, AuditQuery{
		From: base.Add(-time.Hour), To: base.Add(time.Hour), OnlyErrors: true, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 429, rows[0].StatusCode)
	require.Equal(t, "rate_limited", rows[0].ErrorType)
}
