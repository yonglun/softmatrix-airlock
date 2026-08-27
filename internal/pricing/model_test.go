package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// flatPrice 构造一个单档位价格：输入 2 元/百万，输出 8 元/百万，
// 缓存命中 0.5 元/百万，思考 token 与输出同价。
func flatPrice() ModelPrice {
	return ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []Tier{
			{
				MaxInputTokens:   0, // 无上限
				InputPer1M:       2_000_000,
				CachedInputPer1M: 500_000,
				OutputPer1M:      8_000_000,
				ReasoningPer1M:   8_000_000,
			},
		},
	}
}

// tieredPrice 构造按输入长度分档的价格，模拟通义等国产模型的阶梯定价。
func tieredPrice() ModelPrice {
	return ModelPrice{
		Provider:      "dashscope",
		Model:         "qwen-plus",
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []Tier{
			{MaxInputTokens: 128_000, InputPer1M: 800_000, CachedInputPer1M: 160_000, OutputPer1M: 2_000_000, ReasoningPer1M: 2_000_000},
			{MaxInputTokens: 0, InputPer1M: 2_400_000, CachedInputPer1M: 480_000, OutputPer1M: 6_000_000, ReasoningPer1M: 6_000_000},
		},
	}
}

func TestCostSimpleInputOutput(t *testing.T) {
	p := flatPrice()
	// 100 万输入 + 100 万输出 = 2 元 + 8 元 = 10 元 = 10_000_000 Micro
	got, err := p.Cost(Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	require.NoError(t, err)
	require.Equal(t, Micro(10_000_000), got)
}

func TestCostChargesCachedTokensAtCachedRate(t *testing.T) {
	p := flatPrice()
	// 100 万输入中 60 万命中缓存：
	//   未命中 40 万 * 2元/百万 = 0.8 元
	//   命中   60 万 * 0.5元/百万 = 0.3 元
	//   合计 1.1 元 = 1_100_000 Micro
	got, err := p.Cost(Usage{InputTokens: 1_000_000, CachedInputTokens: 600_000})
	require.NoError(t, err)
	require.Equal(t, Micro(1_100_000), got)
}

func TestCostChargesReasoningTokensSeparately(t *testing.T) {
	p := flatPrice()
	p.Tiers[0].ReasoningPer1M = 16_000_000 // 思考 token 单独定价：16 元/百万

	// 100 万输出中 25 万是思考 token：
	//   普通输出 75 万 * 8元/百万  = 6 元
	//   思考     25 万 * 16元/百万 = 4 元
	//   合计 10 元
	got, err := p.Cost(Usage{OutputTokens: 1_000_000, ReasoningTokens: 250_000})
	require.NoError(t, err)
	require.Equal(t, Micro(10_000_000), got)
}

func TestCostZeroUsageIsZero(t *testing.T) {
	got, err := flatPrice().Cost(Usage{})
	require.NoError(t, err)
	require.Equal(t, Micro(0), got)
}

func TestSelectTierPicksByInputTokens(t *testing.T) {
	p := tieredPrice()

	low, err := p.SelectTier(100_000)
	require.NoError(t, err)
	require.Equal(t, Micro(800_000), low.InputPer1M)

	boundary, err := p.SelectTier(128_000)
	require.NoError(t, err)
	require.Equal(t, Micro(800_000), boundary.InputPer1M, "边界值应落在第一档（<=）")

	high, err := p.SelectTier(128_001)
	require.NoError(t, err)
	require.Equal(t, Micro(2_400_000), high.InputPer1M)
}

func TestCostUsesSelectedTier(t *testing.T) {
	p := tieredPrice()
	// 20 万输入落在第二档：20 万 * 2.4元/百万 = 0.48 元 = 480_000 Micro
	got, err := p.Cost(Usage{InputTokens: 200_000})
	require.NoError(t, err)
	require.Equal(t, Micro(480_000), got)
}

func TestSelectTierFailsWhenNoTierMatches(t *testing.T) {
	p := ModelPrice{
		Tiers: []Tier{{MaxInputTokens: 1000, InputPer1M: 1}},
	}
	_, err := p.SelectTier(5000)
	require.Error(t, err)
}

func TestCostFailsWhenNoTiersDefined(t *testing.T) {
	_, err := ModelPrice{}.Cost(Usage{InputTokens: 1})
	require.Error(t, err)
}

func TestCostRejectsCachedExceedingInput(t *testing.T) {
	_, err := flatPrice().Cost(Usage{InputTokens: 100, CachedInputTokens: 200})
	require.Error(t, err)
	require.Contains(t, err.Error(), "缓存")
}

func TestCostRejectsReasoningExceedingOutput(t *testing.T) {
	_, err := flatPrice().Cost(Usage{OutputTokens: 100, ReasoningTokens: 200})
	require.Error(t, err)
	require.Contains(t, err.Error(), "思考")
}

func TestCostRejectsNegativeTokens(t *testing.T) {
	_, err := flatPrice().Cost(Usage{InputTokens: -1})
	require.Error(t, err)
}

func TestMicroString(t *testing.T) {
	require.Equal(t, "0.000000", Micro(0).String())
	require.Equal(t, "1.500000", Micro(1_500_000).String())
	require.Equal(t, "-0.000001", Micro(-1).String())
}
