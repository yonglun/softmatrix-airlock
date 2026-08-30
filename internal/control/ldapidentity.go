package control

import (
	"context"
	"fmt"

	"github.com/go-ldap/ldap/v3"
)

// LDAPIdentitySource 从 LDAP 读用户的启用状态，供离职对账使用。
// external_id 用 uid 属性——需与 OIDC 的 sub 对齐（Casdoor 透传 LDAP uid）。
type LDAPIdentitySource struct {
	cfg LDAPConfig
}

func NewLDAPIdentitySource(cfg LDAPConfig) *LDAPIdentitySource {
	return &LDAPIdentitySource{cfg: cfg}
}

func (s *LDAPIdentitySource) ActiveExternalIDs(ctx context.Context) (map[string]bool, error) {
	conn, err := ldap.DialURL(s.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("连接 LDAP 失败: %w", err)
	}
	defer conn.Close()

	if s.cfg.BindDN != "" {
		if err := conn.Bind(s.cfg.BindDN, s.cfg.BindPass); err != nil {
			return nil, fmt.Errorf("LDAP 绑定失败: %w", err)
		}
	}

	req := ldap.NewSearchRequest(
		s.cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false,
		"(objectClass=inetOrgPerson)",
		[]string{"uid"},
		nil,
	)
	res, err := conn.SearchWithPaging(req, 500)
	if err != nil {
		return nil, fmt.Errorf("LDAP 搜索用户失败: %w", err)
	}

	out := make(map[string]bool, len(res.Entries))
	for _, e := range res.Entries {
		if uid := e.GetAttributeValue("uid"); uid != "" {
			out[uid] = true
		}
	}
	return out, nil
}
