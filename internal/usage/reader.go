package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/softmatrix/airlock/internal/pricing"
)

// 聚合维度。取值是白名单——group_by 由调用方（最终是 URL 参数）给出，
// 绝不能把原值拼进 SQL。
const (
	GroupByOrg   = "org"
	GroupByUser  = "user"
	GroupByModel = "model"
	GroupByDay   = "day"
)

var ErrUnknownGroupBy = errors.New("未知的聚合维度")

// Reader 只读用量表。与 ClickHouseSink 同包但分文件：
// 读写两条路径互不干扰，共享的只有这张表的结构知识。
type Reader struct {
	conn driver.Conn
}

func NewReader(dsn string) (*Reader, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析 ClickHouse DSN 失败: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("连接 ClickHouse 失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("ClickHouse 健康检查失败: %w", err)
	}
	return &Reader{conn: conn}, nil
}

func (r *Reader) Close() error { return r.conn.Close() }

// SummaryQuery 是聚合查询的条件。From/To 必填且由调用方保证有界。
type SummaryQuery struct {
	From, To time.Time
	GroupBy  string
	// OrgIDs 为空表示不限范围；非空表示只统计这些组织。
	OrgIDs []string
}

// SummaryRow 是聚合结果的一行。Key 是维度值。
type SummaryRow struct {
	Key          string
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CostMicro    int64
}

// summaryLimit 是聚合结果的行数上限。维度基数再大也不该把整张表拉进内存；
// 报表是给人看的，超过这个数说明该换个维度或收窄范围。
const summaryLimit = 500

// dimensionSQL 把白名单维度映射成 SQL 片段。
func dimensionSQL(groupBy string) (expr string, orderBy string, err error) {
	switch groupBy {
	case GroupByOrg:
		return "org_id", "cost_micro DESC", nil
	case GroupByUser:
		return "user_id", "cost_micro DESC", nil
	case GroupByModel:
		return "model", "cost_micro DESC", nil
	case GroupByDay:
		// 按天要时间正序——那是给人读趋势的，不是排名。
		return "toString(toDate(ts))", "k ASC", nil
	default:
		return "", "", ErrUnknownGroupBy
	}
}

func (r *Reader) Summary(ctx context.Context, q SummaryQuery) ([]SummaryRow, error) {
	expr, orderBy, err := dimensionSQL(q.GroupBy)
	if err != nil {
		return nil, err
	}

	sql := `SELECT ` + expr + ` AS k,
		    count() AS requests,
		    sum(input_tokens) AS input_tokens,
		    sum(output_tokens) AS output_tokens,
		    sum(cost_micro) AS cost_micro
		FROM airlock.usage_records
		WHERE ts >= ? AND ts < ?`
	args := []any{q.From, q.To}
	if len(q.OrgIDs) > 0 {
		sql += ` AND org_id IN ?`
		args = append(args, q.OrgIDs)
	}
	sql += ` GROUP BY k ORDER BY ` + orderBy + ` LIMIT ` + fmt.Sprint(summaryLimit)

	rows, err := r.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("查询用量聚合失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SummaryRow{}
	for rows.Next() {
		var row SummaryRow
		var requests uint64
		if err := rows.Scan(&row.Key, &requests,
			&row.InputTokens, &row.OutputTokens, &row.CostMicro); err != nil {
			return nil, fmt.Errorf("扫描聚合行失败: %w", err)
		}
		row.Requests = int64(requests)
		out = append(out, row)
	}
	return out, rows.Err()
}

// Cost 把 Micro 还原成 pricing.Micro，供调用方按统一口径处理金额。
func (row SummaryRow) Cost() pricing.Micro { return pricing.Micro(row.CostMicro) }
