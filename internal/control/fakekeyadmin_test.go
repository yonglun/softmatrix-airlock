package control

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/softmatrix/airlock/internal/litellm"
)

// fakeKeyAdmin 是 LiteLLMKeyAdmin 的内存实现。
type fakeKeyAdmin struct {
	mu   sync.Mutex
	keys map[string]litellm.Key

	generateErr error
	existsErr   error
	blockErr    error
	deleteErr   error
	updateErr   error
	// blockNotFound 让 BlockKey 模拟探针 P1 的真实形态：
	// 上游对不存在的 key 返回 404。
	blockNotFound bool

	calls []string
}

func newFakeKeyAdmin() *fakeKeyAdmin {
	return &fakeKeyAdmin{keys: map[string]litellm.Key{}}
}

func (f *fakeKeyAdmin) GenerateKey(_ context.Context, k litellm.Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "generate:"+k.Key)
	if f.generateErr != nil {
		return f.generateErr
	}
	f.keys[k.Key] = k
	return nil
}

func (f *fakeKeyAdmin) KeyExists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "exists:"+key)
	if f.existsErr != nil {
		return false, f.existsErr
	}
	_, ok := f.keys[key]
	return ok, nil
}

func (f *fakeKeyAdmin) BlockKey(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "block:"+key)
	if f.blockNotFound {
		// 探针 P1 的真实形态：上游对不存在的 key 返回 404。
		return &litellm.APIError{Status: 404, Body: `{"error":{"message":"Key not found."}}`}
	}
	return f.blockErr
}

func (f *fakeKeyAdmin) UpdateKeyBudget(_ context.Context, key string, maxBudget float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("update-budget:%g", maxBudget))
	if f.updateErr != nil {
		return f.updateErr
	}
	k := f.keys[key]
	k.MaxBudget = &maxBudget
	f.keys[key] = k
	return nil
}

func (f *fakeKeyAdmin) DeleteKey(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete:"+key)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.keys, key)
	return nil
}

func (f *fakeKeyAdmin) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.keys[key]
	return ok
}

func (f *fakeKeyAdmin) generated(key string) (litellm.Key, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[key]
	return k, ok
}

func (f *fakeKeyAdmin) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeKeyAdmin) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

var errUpstreamDown = errors.New("模拟上游不可用")
