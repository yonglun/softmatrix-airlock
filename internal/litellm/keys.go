package litellm

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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

// KeyExists 回查一把密钥是否存在于上游。
//
// 404 返回 (false, nil)——那是一个确定的答案。其余错误照常返回：
// 把 500 当成「不存在」会让 pending 清理误判并删掉本地行，
// 正好制造出它要消灭的无主凭据。
func (c *Client) KeyExists(ctx context.Context, key string) (bool, error) {
	path := "/key/info?key=" + url.QueryEscape(key)
	if err := c.do(ctx, http.MethodGet, path, nil, nil); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BlockKey 封禁一把密钥：上游随即拒绝它的调用，但记录与用量留存。
// 吊销用它而不用 DeleteKey，是为了保住上游的审计关联。
func (c *Client) BlockKey(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodPost, "/key/block", map[string]any{"key": key}, nil)
}

// DeleteKey 彻底删除一把密钥。只用于清理签发失败留下的残骸。
func (c *Client) DeleteKey(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodPost, "/key/delete", map[string]any{"keys": []string{key}}, nil)
}

// UpdateKeyBudget 改一把密钥的 max_budget。临时提额与到期回收都用它。
//
// /key/update 是部分更新：只发这两个字段不会波及 models、rpm_limit 等
// 其它配置（已实测）。刻意不做成通用的 UpdateKey——多发字段就多一分
// 覆盖掉别处配置的风险，而本阶段只需要改预算这一件事。
func (c *Client) UpdateKeyBudget(ctx context.Context, key string, maxBudget float64) error {
	body := map[string]any{"key": key, "max_budget": maxBudget}
	return c.do(ctx, http.MethodPost, "/key/update", body, nil)
}
