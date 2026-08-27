// Package pricing 定义定价数据模型与成本核算。
//
// 全程整数运算，禁止使用 float——浮点误差会在千万级调用记录上累积成
// 账单对不上的问题。金额单位统一为 Micro（1e-6 元）。
package pricing

import (
	"errors"
	"fmt"
	"time"
)

// Micro 是金额单位，1 Micro = 1e-6 元。
type Micro int64

// String 以元为单位格式化，保留六位小数。
func (m Micro) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%06d", sign, v/1_000_000, v%1_000_000)
}

// Tier 是一个价格档位。国产模型普遍按「本次请求的输入长度」分档计价。
type Tier struct {
	// MaxInputTokens 是本档适用的输入 token 上限（含）。0 表示无上限。
	MaxInputTokens int64
	// 以下四项均为「每百万 token 的价格」，单位 Micro。
	InputPer1M       Micro
	CachedInputPer1M Micro
	OutputPer1M      Micro
	ReasoningPer1M   Micro
}

// ModelPrice 是某个模型在某个生效时间点之后的价格。
type ModelPrice struct {
	Provider      string
	Model         string
	EffectiveFrom time.Time
	Currency      string
	// Tiers 按 MaxInputTokens 升序排列，最后一档可为 0（无上限）。
	// 定价平坦的模型只有一档，MaxInputTokens 为 0。
	Tiers []Tier
}

// Usage 是一次调用的 token 用量。
// CachedInputTokens 是 InputTokens 的子集，ReasoningTokens 是 OutputTokens 的子集，
// 这与 OpenAI 兼容协议中 *_tokens_details 的语义一致。
type Usage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

var ErrNoTier = errors.New("没有匹配的价格档位")

// SelectTier 按本次请求的输入 token 数选出适用档位。
func (p ModelPrice) SelectTier(inputTokens int64) (Tier, error) {
	if len(p.Tiers) == 0 {
		return Tier{}, fmt.Errorf("%w: %s/%s 未定义任何档位", ErrNoTier, p.Provider, p.Model)
	}
	for _, t := range p.Tiers {
		if t.MaxInputTokens == 0 || inputTokens <= t.MaxInputTokens {
			return t, nil
		}
	}
	return Tier{}, fmt.Errorf("%w: %s/%s 输入 %d tokens 超出所有档位上限",
		ErrNoTier, p.Provider, p.Model, inputTokens)
}

// Cost 计算一次调用的成本。
//
// 公式对四类 token 一视同仁，没有条件分支：
//
//	(输入 - 缓存命中) * 输入价 + 缓存命中 * 缓存价
//	  + (输出 - 思考) * 输出价 + 思考 * 思考价
//
// 供应商若不单独为缓存或思考定价，把对应价格设为与输入/输出相同即可。
func (p ModelPrice) Cost(u Usage) (Micro, error) {
	if err := u.validate(); err != nil {
		return 0, err
	}
	tier, err := p.SelectTier(u.InputTokens)
	if err != nil {
		return 0, err
	}

	billableInput := u.InputTokens - u.CachedInputTokens
	billableOutput := u.OutputTokens - u.ReasoningTokens

	total := perMillion(billableInput, tier.InputPer1M) +
		perMillion(u.CachedInputTokens, tier.CachedInputPer1M) +
		perMillion(billableOutput, tier.OutputPer1M) +
		perMillion(u.ReasoningTokens, tier.ReasoningPer1M)

	return total, nil
}

func (u Usage) validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.ReasoningTokens < 0 {
		return fmt.Errorf("token 数不能为负: %+v", u)
	}
	if u.CachedInputTokens > u.InputTokens {
		return fmt.Errorf("缓存命中 token（%d）不能超过输入 token（%d）", u.CachedInputTokens, u.InputTokens)
	}
	if u.ReasoningTokens > u.OutputTokens {
		return fmt.Errorf("思考 token（%d）不能超过输出 token（%d）", u.ReasoningTokens, u.OutputTokens)
	}
	return nil
}

// perMillion 计算 tokens 个 token 按 pricePer1M 的价格所需金额。
// 整数除法向零取整，误差最大 1 Micro（1e-6 元），可忽略。
func perMillion(tokens int64, pricePer1M Micro) Micro {
	return Micro(tokens * int64(pricePer1M) / 1_000_000)
}
