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
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000), // 1 月 10 日降价
	})

	early, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), early.Tiers[0].InputPer1M, "1 月 5 日应适用旧价")

	late, err := tbl.Lookup("deepseek", "deepseek-chat", jan(15))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), late.Tiers[0].InputPer1M, "1 月 15 日应适用新价")
}

func TestMemoryTableBoundaryUsesNewPrice(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000),
	})
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(10))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), got.Tiers[0].InputPer1M, "生效当刻应适用新价")
}

func TestMemoryTableIgnoresInsertionOrder(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(10), 1_000_000),
		priceAt(jan(1), 2_000_000), // 乱序插入
	})
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), got.Tiers[0].InputPer1M)
}

func TestMemoryTableFailsBeforeAnyPriceTakesEffect(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{priceAt(jan(10), 1_000_000)})
	_, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}

func TestMemoryTableFailsForUnknownModel(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{priceAt(jan(1), 1_000_000)})
	_, err := tbl.Lookup("openai", "gpt-4o", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}
