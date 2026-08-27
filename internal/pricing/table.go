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

// validateTierOrder 确保 ModelPrice 的档位按 MaxInputTokens 升序排列，
// 无上限档位（MaxInputTokens=0）必须是最后一档。这是 SelectTier 的前置条件。
func validateTierOrder(p ModelPrice) error {
	if len(p.Tiers) == 0 {
		return nil // 空档位列表是有效的（虽然后续调用会报错）
	}
	for i := 1; i < len(p.Tiers); i++ {
		prev, cur := p.Tiers[i-1], p.Tiers[i]
		if prev.MaxInputTokens == 0 {
			return fmt.Errorf("%s/%s: 无上限档位（MaxInputTokens=0）必须是最后一档", p.Provider, p.Model)
		}
		if cur.MaxInputTokens != 0 && cur.MaxInputTokens <= prev.MaxInputTokens {
			return fmt.Errorf("%s/%s: 价格档位必须按 MaxInputTokens 严格升序排列", p.Provider, p.Model)
		}
	}
	return nil
}

func NewMemoryTable(prices []ModelPrice) (*MemoryTable, error) {
	byModel := make(map[string][]ModelPrice)
	for _, p := range prices {
		if err := validateTierOrder(p); err != nil {
			return nil, err
		}
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
	return &MemoryTable{byModel: byModel}, nil
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
