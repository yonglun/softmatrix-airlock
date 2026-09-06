package control

import (
	"context"
	"fmt"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig 是接入任意标准 OIDC provider 所需的全部信息。
// 换 IdP 只改这几个值，代码零改动。
type OIDCConfig struct {
	Issuer string
	// DiscoveryURL 是发起 discovery 请求实际连接的地址。留空时与 Issuer
	// 相同——绝大多数部署（含开发环境）两者本来就是同一个值。
	//
	// 两者不同，是为了适配「OIDC provider 与本进程同在一套 docker
	// compose 里跑」这种容器化部署：provider 自己上报的 issuer 必须是
	// 浏览器能访问到的地址（客户填在自己的 provider 配置里），但本进程
	// 发起 discovery 请求时只能走 compose 网络内部的服务名——这两个
	// 地址不可能是同一个字符串。P1.5c 打包验收时真实撞见过这个坑：
	// 把 Issuer 改成内部地址会导致 provider 自报的 issuer 对不上、
	// discovery 直接被拒；留成浏览器地址，本进程又连不通。
	DiscoveryURL string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// OIDCClient 是授权码流程的客户端。
type OIDCClient interface {
	AuthCodeURL(state, challenge string) string
	Exchange(ctx context.Context, code, verifier string) (*Identity, error)
}

type oidcClient struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// NewOIDCClient 通过 discovery 拉取 provider 元数据并构造客户端。
// issuer 不可达或不是合法 OIDC provider 时立即失败——
// 与 ClickHouse 的 Ping 同理：配置错误要在启动阶段炸，不要等到第一个用户登录。
func NewOIDCClient(ctx context.Context, cfg OIDCConfig) (OIDCClient, error) {
	discoveryURL := cfg.DiscoveryURL
	if discoveryURL == "" {
		discoveryURL = cfg.Issuer
	}
	internal := discoveryURL != cfg.Issuer

	if internal {
		// go-oidc 硬性要求 discovery 响应里的 issuer 字段与请求 URL 完全
		// 一致，这里显式告诉它「按 discoveryURL 连接，但认 cfg.Issuer
		// 作校验用的 issuer」——这是库自己为「discovery 地址与自报
		// issuer 不一致」这类场景开的口子（常见于 Azure 等 off-spec
		// provider，这里用于容器化部署的内外网地址分离）。
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.Issuer)
	}

	provider, err := oidc.NewProvider(ctx, discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery 失败（discovery_url=%s, issuer=%s）: %w",
			discoveryURL, cfg.Issuer, err)
	}

	endpoint := provider.Endpoint()
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	if internal {
		// token 端点是本进程自己发起的服务端调用（Exchange），必须走
		// 与 discovery 同一条内部可达的路径，不能是浏览器地址——但
		// provider 上报的 token_endpoint 跟着 Issuer 的 origin 走（与
		// authorization_endpoint 同理），所以要重写。
		// authorization_endpoint（AuthCodeURL 用的那个）不动：那是给
		// 浏览器的重定向目标，必须保持外部可达的原样。
		endpoint.TokenURL, err = rewriteOrigin(endpoint.TokenURL, cfg.Issuer, discoveryURL)
		if err != nil {
			return nil, fmt.Errorf("重写 token 端点失败: %w", err)
		}

		// 拉 JWKS 验签名同理，也是本进程自己发起的服务端调用。
		// provider.Verifier() 内部认的 jwks_uri 是私有字段，没有公开的
		// 改法，只能拿到原始 discovery 文档里的 jwks_uri、重写 origin、
		// 再手工拼一个 verifier——用的还是 cfg.Issuer 校验 ID token 的
		// iss claim，只是换一条内部可达的路径去取公钥。
		var doc struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := provider.Claims(&doc); err != nil {
			return nil, fmt.Errorf("解析 discovery 文档失败: %w", err)
		}
		jwksURL, err := rewriteOrigin(doc.JWKSURI, cfg.Issuer, discoveryURL)
		if err != nil {
			return nil, fmt.Errorf("重写 JWKS 端点失败: %w", err)
		}
		keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
		verifier = oidc.NewVerifier(cfg.Issuer, keySet, &oidc.Config{ClientID: cfg.ClientID})
	}

	return &oidcClient{
		provider: provider,
		verifier: verifier,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     endpoint,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

// rewriteOrigin 把 target 的 scheme+host 从 fromOrigin 换成 toOrigin，
// 保留 target 原有的 path/query。
//
// 只在 target 确实落在 fromOrigin 下时才重写，防止意外改写一个本来就
// 不属于该 provider 的地址（理论上不应发生，属于防御性判断）。
func rewriteOrigin(target, fromOrigin, toOrigin string) (string, error) {
	t, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("解析目标地址 %q 失败: %w", target, err)
	}
	from, err := url.Parse(fromOrigin)
	if err != nil {
		return "", fmt.Errorf("解析原地址 %q 失败: %w", fromOrigin, err)
	}
	to, err := url.Parse(toOrigin)
	if err != nil {
		return "", fmt.Errorf("解析目标地址 %q 失败: %w", toOrigin, err)
	}
	if t.Scheme != from.Scheme || t.Host != from.Host {
		return target, nil
	}
	t.Scheme = to.Scheme
	t.Host = to.Host
	return t.String(), nil
}

func (c *oidcClient) AuthCodeURL(state, challenge string) string {
	return c.oauth.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Exchange 用授权码换 token，并校验 ID token 的签名、issuer 与 audience。
func (c *oidcClient) Exchange(ctx context.Context, code, verifier string) (*Identity, error) {
	tok, err := c.oauth.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return nil, fmt.Errorf("授权码换 token 失败: %w", err)
	}

	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, fmt.Errorf("token 响应中缺少 id_token")
	}

	idToken, err := c.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("ID token 校验失败: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("解析 ID token claims 失败: %w", err)
	}

	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}

	return &Identity{
		Subject:       idToken.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		DisplayName:   name,
	}, nil
}
