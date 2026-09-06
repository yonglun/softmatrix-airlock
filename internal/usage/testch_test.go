package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"github.com/softmatrix/airlock/internal/pricing"
)

// testCH 连真实 ClickHouse。没设 CLICKHOUSE_DSN 就跳过——
// 与 internal/control 的 testDB 同一惯例。
//
// 注意：make test 平常不传这个变量，因此这些测试默认会 skip。
// 验收时必须带上 CLICKHOUSE_DSN 跑一次，才算真的验过聚合 SQL。
func testCH(t *testing.T) driver.Conn {
	t.Helper()
	dsn := os.Getenv("CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("未设置 CLICKHOUSE_DSN，跳过需要 ClickHouse 的集成测试")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	require.NoError(t, err)
	conn, err := clickhouse.Open(opts)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, conn.Ping(ctx))

	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// cleanUsage 清空用量表。ClickHouse 的 DELETE 是异步变更，
// 这里用 TRUNCATE：测试要的是确定的起点，不是最终一致。
func cleanUsage(t *testing.T, conn driver.Conn) {
	t.Helper()
	require.NoError(t, conn.Exec(context.Background(),
		`TRUNCATE TABLE IF EXISTS airlock.usage_records`))
}

// seedUsage 插一条用量记录。ts 之外的字段给出可辨识的默认值。
func seedUsage(t *testing.T, conn driver.Conn, r Record) {
	t.Helper()
	sink := &ClickHouseSink{conn: conn}
	require.NoError(t, sink.InsertBatch(context.Background(), []Record{r}))
}

func usageAt(ts time.Time, orgID, userID, model string, cost int64) Record {
	return Record{
		RequestID: orgID + "-" + userID + "-" + ts.Format(time.RFC3339Nano),
		Timestamp: ts, OrgID: orgID, UserID: userID, KeyID: "k-" + orgID,
		Provider: "dashscope", Model: model,
		Usage:      pricingUsage(10, 20),
		CostMicro:  micro(cost),
		StatusCode: 200, LatencyMS: 100, TTFTMS: 30,
	}
}

func pricingUsage(in, out int64) pricing.Usage {
	return pricing.Usage{InputTokens: in, OutputTokens: out}
}

func micro(v int64) pricing.Micro { return pricing.Micro(v) }
