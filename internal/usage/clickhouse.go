package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// ClickHouseSink 把用量记录批量写入 ClickHouse。
type ClickHouseSink struct {
	conn driver.Conn
}

func NewClickHouseSink(dsn string) (*ClickHouseSink, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析 ClickHouse DSN 失败: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("连接 ClickHouse 失败: %w", err)
	}

	// clickhouse.Open 本身是懒连接，不会真的拨号——用 Ping 在启动阶段
	// 就暴露 DSN 错误或网络不通，避免用量批次悄悄写失败到很久之后才在
	// 日志里发现，此前所有批次的计费数据已经静默丢失。
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("ClickHouse 健康检查失败: %w", err)
	}

	return &ClickHouseSink{conn: conn}, nil
}

const insertSQL = `INSERT INTO airlock.usage_records (
	request_id, ts, org_id, user_id, key_id, provider, model,
	input_tokens, cached_input_tokens, output_tokens, reasoning_tokens,
	cost_micro, status_code, latency_ms, ttft_ms, stream, error_type
)`

func (s *ClickHouseSink) InsertBatch(ctx context.Context, records []Record) error {
	batch, err := s.conn.PrepareBatch(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("准备批次失败: %w", err)
	}
	for _, r := range records {
		var stream uint8
		if r.Stream {
			stream = 1
		}
		if err := batch.Append(
			r.RequestID, r.Timestamp, r.OrgID, r.UserID, r.KeyID, r.Provider, r.Model,
			r.Usage.InputTokens, r.Usage.CachedInputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens,
			int64(r.CostMicro), uint16(r.StatusCode), uint32(r.LatencyMS), uint32(r.TTFTMS),
			stream, r.ErrorType,
		); err != nil {
			return fmt.Errorf("追加记录失败: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("发送批次失败: %w", err)
	}
	return nil
}

func (s *ClickHouseSink) Close() error {
	return s.conn.Close()
}
