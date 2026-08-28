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

	// mu 保证 Write 的「检查已关闭 + 发送」与 Close 的「标记已关闭 + 关闭 channel」
	// 不会交错——Write 全程持有读锁，Close 必须拿到写锁才能关闭 channel，
	// 因此 Close 关闭 channel 时，不可能还有正在发送中的 Write 落在检查和发送之间的窗口里。
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
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
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
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
		w.mu.Lock()
		w.closed = true
		close(w.queue)
		w.mu.Unlock()
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
