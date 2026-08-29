package control

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig 是接入任意标准 OIDC provider 所需的全部信息。
// 换 IdP 只改这几个值，代码零改动。
type OIDCConfig struct {
	Issuer       string
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
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery 失败（issuer=%s）: %w", cfg.Issuer, err)
	}

	return &oidcClient{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
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
		Subject:     idToken.Subject,
		Email:       claims.Email,
		DisplayName: name,
	}, nil
}
