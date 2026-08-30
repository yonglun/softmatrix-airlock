package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// LDAPConfig 是连接 LDAP 通讯录所需的配置。
type LDAPConfig struct {
	URL      string // 如 ldap://localhost:389
	BindDN   string
	BindPass string
	BaseDN   string // 组织单元的搜索起点，如 dc=example,dc=org
	Filter   string // 默认 (objectClass=organizationalUnit)
	NameAttr string // 默认 ou
}

// LDAPSource 从 LDAP 读组织单元树。
// 它只做协议层的取数与字段映射，不含任何差异判定逻辑——
// 那些全在 ComputeDiff 里，是可以脱离网络完整单测的纯函数。
type LDAPSource struct {
	cfg LDAPConfig
}

func NewLDAPSource(cfg LDAPConfig) *LDAPSource {
	if cfg.Filter == "" {
		cfg.Filter = "(objectClass=organizationalUnit)"
	}
	if cfg.NameAttr == "" {
		cfg.NameAttr = "ou"
	}
	return &LDAPSource{cfg: cfg}
}

func (s *LDAPSource) Name() string { return "ldap" }

// FetchOrgTree 拉取全部组织单元，用 DN 作为 external_id，
// 父节点 DN 作为 ParentExternalID —— DN 天然编码了层级关系。
func (s *LDAPSource) FetchOrgTree(ctx context.Context) ([]ExternalOrgNode, error) {
	conn, err := ldap.DialURL(s.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("连接 LDAP 失败（%s）: %w", s.cfg.URL, err)
	}
	defer conn.Close()

	if s.cfg.BindDN != "" {
		if err := conn.Bind(s.cfg.BindDN, s.cfg.BindPass); err != nil {
			return nil, fmt.Errorf("LDAP 绑定失败（%s）: %w", s.cfg.BindDN, err)
		}
	}

	req := ldap.NewSearchRequest(
		s.cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false,
		s.cfg.Filter,
		[]string{s.cfg.NameAttr},
		nil,
	)

	res, err := conn.SearchWithPaging(req, 500)
	if err != nil {
		return nil, fmt.Errorf("LDAP 搜索失败（base=%s）: %w", s.cfg.BaseDN, err)
	}

	// 先收集本次结果里出现的全部 DN，用于判断某个父 DN 是否也在结果集内。
	present := make(map[string]bool, len(res.Entries))
	for _, e := range res.Entries {
		present[strings.ToLower(e.DN)] = true
	}

	nodes := make([]ExternalOrgNode, 0, len(res.Entries))
	for _, e := range res.Entries {
		name := e.GetAttributeValue(s.cfg.NameAttr)
		if name == "" {
			name = e.DN
		}
		parent := parentDN(e.DN)
		// 父 DN 不在结果集里（通常是 BaseDN 本身）时，视为根节点
		if !present[strings.ToLower(parent)] {
			parent = ""
		}
		nodes = append(nodes, ExternalOrgNode{
			ExternalID:       e.DN,
			ParentExternalID: parent,
			Name:             name,
		})
	}
	return nodes, nil
}

// parentDN 去掉 DN 的第一段，得到父节点 DN。
// 例：ou=gw,ou=plat,dc=example,dc=org → ou=plat,dc=example,dc=org
func parentDN(dn string) string {
	i := strings.Index(dn, ",")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(dn[i+1:])
}
