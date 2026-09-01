package litellm

import (
	"context"
	"net/http"
)

// Key 是一把 LiteLLM 虚拟密钥的签发参数。
//
// Models 不带 omitempty：上游把该字段缺失或为空一律当作「放行全部模型」，
// 是 fail-open 的。始终发出它，让「漏发导致签出不受限密钥」在客户端这层
// 就不可能发生。其余可选字段用指针 + omitempty，未设置就不发。
type Key struct {
	Key            string   `json:"key,omitempty"`
	KeyAlias       string   `json:"key_alias,omitempty"`
	TeamID         *string  `json:"team_id,omitempty"`
	Models         []string `json:"models"`
	MaxBudget      *float64 `json:"max_budget,omitempty"`
	BudgetDuration *string  `json:"budget_duration,omitempty"`
	RPMLimit       *int     `json:"rpm_limit,omitempty"`
	TPMLimit       *int     `json:"tpm_limit,omitempty"`
	// Duration 是相对时长（如 "3600s"）。上游会静默丢弃绝对时间 expires，
	// 因此过期时间必须换算成相对时长下发。
	Duration *string `json:"duration,omitempty"`
}

// GenerateKey 按调用方指定的 key 值签发一把上游密钥。
//
// 注意：用已存在的 key 值重复调用会返回 200 但**静默不做任何事**，
// 且响应体会回显你传入的新参数。因此重试是安全的，但绝不能据响应体
// 断定「新参数已生效」。
func (c *Client) GenerateKey(ctx context.Context, k Key) error {
	if k.Models == nil {
		k.Models = []string{}
	}
	return c.do(ctx, http.MethodPost, "/key/generate", k, nil)
}
