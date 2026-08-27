package pricing

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrPriceNotFound = errors.New("未找到生效价格")

// Table 按 (供应商, 模型, 时刻) 查询当时生效的价格。
type Table interface {
	Lookup(provider, model string, at time.Time) (ModelPrice, error)
}

// MemoryTable 是内存实现。价格数据量很小（几百条），全量常驻内存即可，
// 避免每次调用都查库。价格变更时重新构造一个 Table 替换。
type MemoryTable struct {
	// byModel 的 value 按 EffectiveFrom 升序排列。
	byModel map[string][]ModelPrice
}

func NewMemoryTable(prices []ModelPrice) *MemoryTable {
	byModel := make(map[string][]ModelPrice)
	for _, p := range prices {
		k := modelKey(p.Provider, p.Model)
		byModel[k] = append(byModel[k], p)
	}
	for k := range byModel {
		versions := byModel[k]
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].EffectiveFrom.Before(versions[j].EffectiveFrom)
		})
		byModel[k] = versions
	}
	return &MemoryTable{byModel: byModel}
}

// Lookup 返回 at 时刻生效的价格，即 EffectiveFrom <= at 中最晚的那条。
func (t *MemoryTable) Lookup(provider, model string, at time.Time) (ModelPrice, error) {
	versions, ok := t.byModel[modelKey(provider, model)]
	if !ok {
		return ModelPrice{}, fmt.Errorf("%w: 无 %s/%s 的任何价格记录", ErrPriceNotFound, provider, model)
	}
	var chosen *ModelPrice
	for i := range versions {
		if versions[i].EffectiveFrom.After(at) {
			break
		}
		chosen = &versions[i]
	}
	if chosen == nil {
		return ModelPrice{}, fmt.Errorf("%w: %s/%s 在 %s 尚无生效价格",
			ErrPriceNotFound, provider, model, at.Format(time.RFC3339))
	}
	return *chosen, nil
}

func modelKey(provider, model string) string {
	return provider + "/" + model
}
