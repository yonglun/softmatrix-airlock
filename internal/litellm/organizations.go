package litellm

import (
	"context"
	"net/http"
)

// Organization 是 LiteLLM 的组织实体。
//
// 只保留 Airlock 会读写的两个字段——上游返回体有二十多个字段，
// 全量映射会让本包被上游的 schema 变动牵着走。
type Organization struct {
	ID    string `json:"organization_id"`
	Alias string `json:"organization_alias"`
}

// ListOrganizations 拉取全部组织。上游返回裸数组。
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var out []Organization
	if err := c.do(ctx, http.MethodGet, "/organization/list", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateOrganization 按调用方指定的 ID 建组织。
func (c *Client) CreateOrganization(ctx context.Context, o Organization) error {
	return c.do(ctx, http.MethodPost, "/organization/new", o, nil)
}

// UpdateOrganization 改组织的 alias。
func (c *Client) UpdateOrganization(ctx context.Context, o Organization) error {
	return c.do(ctx, http.MethodPatch, "/organization/update", o, nil)
}

// DeleteOrganization 删除一个组织。
//
// 注意：上游会连带删除挂在该组织下的全部 Team（已实测，且静默返回 200）。
// 调用方必须自己保证这是想要的效果——见 control 侧删除传播的安全性论证。
func (c *Client) DeleteOrganization(ctx context.Context, id string) error {
	body := map[string]any{"organization_ids": []string{id}}
	return c.do(ctx, http.MethodDelete, "/organization/delete", body, nil)
}
