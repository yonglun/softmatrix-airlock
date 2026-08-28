// Package usage 定义调用用量记录及其写入管线。
package usage

import (
	"context"
	"time"

	"github.com/softmatrix/airlock/internal/pricing"
)

// Record 是一次调用的完整用量与审计元数据。
type Record struct {
	RequestID  string
	Timestamp  time.Time
	OrgID      string
	UserID     string
	KeyID      string
	Provider   string
	Model      string
	Usage      pricing.Usage
	CostMicro  pricing.Micro
	StatusCode int
	LatencyMS  int
	TTFTMS     int
	Stream     bool
	ErrorType  string
}

// Sink 是记录的最终去处，由 ClickHouse 等实现。
type Sink interface {
	InsertBatch(ctx context.Context, records []Record) error
}

// Writer 接收单条记录。实现必须是非阻塞的——推理链路不能被写入拖慢。
type Writer interface {
	Write(r Record)
}
