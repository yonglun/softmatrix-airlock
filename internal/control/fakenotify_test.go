package control

import (
	"context"
	"errors"
	"sync"

	"github.com/softmatrix/airlock/internal/notify"
)

// fakeSender 是 notify.Sender 的内存实现。
type fakeSender struct {
	mu   sync.Mutex
	sent []notify.Message
	err  error
}

func newFakeSender() *fakeSender { return &fakeSender{} }

func (f *fakeSender) Channel() string { return "email" }

func (f *fakeSender) Send(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeSender) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSender) snapshot() []notify.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notify.Message, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeSender) recipients() []string {
	out := []string{}
	for _, m := range f.snapshot() {
		out = append(out, m.Recipient)
	}
	return out
}

var errSMTPDown = errors.New("模拟邮件服务器不可用")
