package usage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memSink struct {
	mu      sync.Mutex
	batches [][]Record
}

func (s *memSink) InsertBatch(_ context.Context, records []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Record, len(records))
	copy(cp, records)
	s.batches = append(s.batches, cp)
	return nil
}

func (s *memSink) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func (s *memSink) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

func rec(id string) Record {
	return Record{RequestID: id, Timestamp: time.Now(), Model: "deepseek-chat"}
}

func TestBatchWriterFlushesWhenBatchFull(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 3, time.Hour) // 靠数量触发，不靠时间
	defer w.Close()

	w.Write(rec("a"))
	w.Write(rec("b"))
	w.Write(rec("c"))

	require.Eventually(t, func() bool { return sink.batchCount() == 1 }, time.Second, 10*time.Millisecond)
	require.Len(t, sink.all(), 3)
}

func TestBatchWriterFlushesOnInterval(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 1000, 50*time.Millisecond) // 靠时间触发
	defer w.Close()

	w.Write(rec("a"))

	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 10*time.Millisecond)
}

func TestCloseFlushesPendingRecords(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 1000, time.Hour)

	w.Write(rec("a"))
	w.Write(rec("b"))
	require.NoError(t, w.Close())

	require.Len(t, sink.all(), 2, "Close 必须把未满的批次刷出去")
}

func TestWriteAfterCloseDoesNotPanic(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 10, time.Hour)
	require.NoError(t, w.Close())

	require.NotPanics(t, func() { w.Write(rec("late")) })
}

func TestBatchWriterDropsWhenQueueFullRatherThanBlocking(t *testing.T) {
	// sink 阻塞不返回，模拟 ClickHouse 挂掉。
	// 写入必须继续返回，不能拖垮推理链路。
	blocked := make(chan struct{})
	sink := blockingSink{release: blocked}
	w := NewBatchWriter(sink, 1, time.Hour)
	defer func() { close(blocked); _ = w.Close() }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < queueCapacity*2; i++ {
			w.Write(rec("x"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write 在下游阻塞时被卡住了，必须丢弃而非阻塞")
	}
	require.Positive(t, w.Dropped(), "应记录被丢弃的条数")
}

type blockingSink struct{ release chan struct{} }

func (s blockingSink) InsertBatch(_ context.Context, _ []Record) error {
	<-s.release
	return nil
}
