package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// queueCapacity 是内存队列容量。队列满时新记录被丢弃并计数，
// 绝不阻塞调用方——丢几条用量记录，好过拖垮线上推理。
const queueCapacity = 4096

// flushTimeout 是单次批量写入的超时。
const flushTimeout = 10 * time.Second

// BatchWriter 把记录攒批后异步写入 Sink。
type BatchWriter struct {
	sink          Sink
	queue         chan Record
	batchSize     int
	flushInterval time.Duration

	dropped atomic.Int64

	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}
}

func NewBatchWriter(sink Sink, batchSize int, flushInterval time.Duration) *BatchWriter {
	w := &BatchWriter{
		sink:          sink,
		queue:         make(chan Record, queueCapacity),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		done:          make(chan struct{}),
	}
	go w.run()
	return w
}

// Write 是非阻塞的。队列满或已关闭时丢弃记录并计数。
func (w *BatchWriter) Write(r Record) {
	if w.closed.Load() {
		w.dropped.Add(1)
		return
	}
	select {
	case w.queue <- r:
	default:
		w.dropped.Add(1)
	}
}

// Dropped 返回累计被丢弃的记录条数，用于暴露成监控指标。
func (w *BatchWriter) Dropped() int64 {
	return w.dropped.Load()
}

// Close 停止接收新记录，把队列中剩余的刷出去后返回。
func (w *BatchWriter) Close() error {
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.queue)
		<-w.done
	})
	return nil
}

func (w *BatchWriter) run() {
	defer close(w.done)

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	batch := make([]Record, 0, w.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		if err := w.sink.InsertBatch(ctx, batch); err != nil {
			w.dropped.Add(int64(len(batch)))
			slog.Error("用量记录批量写入失败", "count", len(batch), "err", err)
		}
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case r, ok := <-w.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= w.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
