package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func jan(day int) time.Time {
	return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
}

func priceAt(from time.Time, input Micro) ModelPrice {
	return ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: from,
		Currency:      "CNY",
		Tiers:         []Tier{{MaxInputTokens: 0, InputPer1M: input, OutputPer1M: input * 4}},
	}
}

func TestMemoryTableReturnsLatestEffectivePrice(t *testing.T) {
	tbl, err := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000), // 1 月 10 日降价
	})
	require.NoError(t, err)

	early, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), early.Tiers[0].InputPer1M, "1 月 5 日应适用旧价")

	late, err := tbl.Lookup("deepseek", "deepseek-chat", jan(15))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), late.Tiers[0].InputPer1M, "1 月 15 日应适用新价")
}

func TestMemoryTableBoundaryUsesNewPrice(t *testing.T) {
	tbl, err := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000),
	})
	require.NoError(t, err)
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(10))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), got.Tiers[0].InputPer1M, "生效当刻应适用新价")
}

func TestMemoryTableIgnoresInsertionOrder(t *testing.T) {
	tbl, err := NewMemoryTable([]ModelPrice{
		priceAt(jan(10), 1_000_000),
		priceAt(jan(1), 2_000_000), // 乱序插入
	})
	require.NoError(t, err)
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), got.Tiers[0].InputPer1M)
}

func TestMemoryTableFailsBeforeAnyPriceTakesEffect(t *testing.T) {
	tbl, err := NewMemoryTable([]ModelPrice{priceAt(jan(10), 1_000_000)})
	require.NoError(t, err)
	_, err = tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}

func TestMemoryTableFailsForUnknownModel(t *testing.T) {
	tbl, err := NewMemoryTable([]ModelPrice{priceAt(jan(1), 1_000_000)})
	require.NoError(t, err)
	_, err = tbl.Lookup("openai", "gpt-4o", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}

func TestMemoryTableRejectsUnsortedTiers(t *testing.T) {
	// 档位按 MaxInputTokens 倒序（不合法）
	unsortedPrice := ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: jan(1),
		Currency:      "CNY",
		Tiers: []Tier{
			{MaxInputTokens: 3000, InputPer1M: 1_000_000, OutputPer1M: 4_000_000},
			{MaxInputTokens: 1000, InputPer1M: 2_000_000, OutputPer1M: 8_000_000},
		},
	}
	_, err := NewMemoryTable([]ModelPrice{unsortedPrice})
	require.Error(t, err)
	require.Contains(t, err.Error(), "升序排列")
}

func TestMemoryTableRejectsUnlimitedTierNotLast(t *testing.T) {
	// 无上限档（MaxInputTokens=0）不在最后（不合法）
	invalidPrice := ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: jan(1),
		Currency:      "CNY",
		Tiers: []Tier{
			{MaxInputTokens: 0, InputPer1M: 1_000_000, OutputPer1M: 4_000_000},
			{MaxInputTokens: 3000, InputPer1M: 2_000_000, OutputPer1M: 8_000_000},
		},
	}
	_, err := NewMemoryTable([]ModelPrice{invalidPrice})
	require.Error(t, err)
	require.Contains(t, err.Error(), "最后一档")
}

func TestMemoryTableAcceptsValidTiers(t *testing.T) {
	// 档位升序排列，无上限档在最后（合法）
	validPrice := ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: jan(1),
		Currency:      "CNY",
		Tiers: []Tier{
			{MaxInputTokens: 1000, InputPer1M: 2_000_000, OutputPer1M: 8_000_000},
			{MaxInputTokens: 3000, InputPer1M: 1_000_000, OutputPer1M: 4_000_000},
			{MaxInputTokens: 0, InputPer1M: 500_000, OutputPer1M: 2_000_000},
		},
	}
	tbl, err := NewMemoryTable([]ModelPrice{validPrice})
	require.NoError(t, err)

	// 验证能正确查询
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(1))
	require.NoError(t, err)
	require.Equal(t, 3, len(got.Tiers))
}
