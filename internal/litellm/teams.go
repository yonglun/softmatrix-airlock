package litellm

import (
	"context"
	"net/http"
)

// Team 是 LiteLLM 的团队实体。
//
// OrganizationID 用指针：上游允许 Team 不挂任何组织（返回 null），
// 而空字符串会被当成「一个不存在的组织」从而被 400 拒绝，两者必须区分。
type Team struct {
	ID             string  `json:"team_id"`
	Alias          string  `json:"team_alias"`
	OrganizationID *string `json:"organization_id,omitempty"`
}

// ListTeams 拉取全部团队。上游返回裸数组。
func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	var out []Team
	if err := c.do(ctx, http.MethodGet, "/team/list", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTeam 按调用方指定的 ID 建团队。
//
// OrganizationID 指向的组织必须已经存在，否则上游 400 拒绝——
// 这条外键校验是同步「先建组织再建团队」顺序约束的来源。
func (c *Client) CreateTeam(ctx context.Context, t Team) error {
	return c.do(ctx, http.MethodPost, "/team/new", t, nil)
}

// UpdateTeam 改团队的 alias 与所属组织。
//
// organization_id 可以原地改，team_id 保持不变——因此跨子树移动节点
// 不需要删了重建，绑在该团队上的 Key 也不受影响。
func (c *Client) UpdateTeam(ctx context.Context, t Team) error {
	return c.do(ctx, http.MethodPost, "/team/update", t, nil)
}

// DeleteTeam 删除一个团队。
func (c *Client) DeleteTeam(ctx context.Context, id string) error {
	body := map[string]any{"team_ids": []string{id}}
	return c.do(ctx, http.MethodPost, "/team/delete", body, nil)
}
