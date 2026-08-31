// Package config 从环境变量加载 Airlock 的运行配置。
// 不依赖任何第三方库，便于在测试中注入取值函数。
package config

import (
	"encoding/base64"
	"fmt"
	"time"
)

// Getenv 是取环境变量的函数，测试可注入内存实现。
type Getenv func(key string) string

type Config struct {
	EdgeListenAddr    string
	UpstreamBaseURL   string
	PostgresDSN       string
	ClickHouseDSN     string
	EncryptionKey     []byte
	ControlListenAddr string
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	BootstrapAdmin    string
	ReconcileInterval time.Duration
	// LiteLLM 管理 API。与 UpstreamBaseURL 刻意分开——后者是 Edge 热路径的
	// 配置，本阶段对 Edge 零改动。
	LiteLLMBaseURL string
	// LiteLLMMasterKey 能创建和删除任意组织、团队与密钥，是全系统权限最高的
	// 凭据。为空时同步整体禁用。
	LiteLLMMasterKey    string
	LiteLLMSyncInterval time.Duration
}

const encryptionKeyLen = 32

func Load(getenv Getenv) (Config, error) {
	cfg := Config{
		EdgeListenAddr:    or(getenv("EDGE_LISTEN_ADDR"), ":8080"),
		UpstreamBaseURL:   or(getenv("EDGE_UPSTREAM_BASE_URL"), "http://localhost:4000"),
		PostgresDSN:       getenv("POSTGRES_DSN"),
		ClickHouseDSN:     getenv("CLICKHOUSE_DSN"),
		ControlListenAddr: or(getenv("CONTROL_LISTEN_ADDR"), ":8081"),
		OIDCIssuer:        getenv("OIDC_ISSUER"),
		OIDCClientID:      getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:  getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:   getenv("OIDC_REDIRECT_URL"),
		BootstrapAdmin:    getenv("AIRLOCK_BOOTSTRAP_ADMIN"),
		LiteLLMBaseURL:    or(getenv("LITELLM_BASE_URL"), "http://localhost:4000"),
		LiteLLMMasterKey:  getenv("LITELLM_MASTER_KEY"),
	}

	if raw := getenv("AIRLOCK_ENCRYPTION_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return Config{}, fmt.Errorf("AIRLOCK_ENCRYPTION_KEY 不是合法的 base64: %w", err)
		}
		if len(key) != encryptionKeyLen {
			return Config{}, fmt.Errorf("AIRLOCK_ENCRYPTION_KEY 解码后必须是 %d 字节，实际 %d 字节", encryptionKeyLen, len(key))
		}
		cfg.EncryptionKey = key
	}

	cfg.ReconcileInterval = 5 * time.Minute
	if raw := getenv("RECONCILE_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("RECONCILE_INTERVAL 不是合法的时间间隔（如 5m、30s）: %w", err)
		}
		// time.NewTicker 在 d <= 0 时 panic，且那个 panic 发生在对账循环的
		// goroutine 里、没有 recover，会让整个进程崩溃。
		// 关掉对账的正确做法是留空 LDAP_URL，不是把周期设成 0。
		if d <= 0 {
			return Config{}, fmt.Errorf(
				"RECONCILE_INTERVAL 必须为正数，当前为 %s；"+
					"如需关闭离职对账，请改为不设置 LDAP_URL", raw)
		}
		cfg.ReconcileInterval = d
	}

	cfg.LiteLLMSyncInterval = 5 * time.Minute
	if raw := getenv("LITELLM_SYNC_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("LITELLM_SYNC_INTERVAL 不是合法的时间间隔（如 5m、30s）: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf(
				"LITELLM_SYNC_INTERVAL 必须为正数，当前为 %s；"+
					"如需关闭同步，请改为不设置 LITELLM_MASTER_KEY", raw)
		}
		cfg.LiteLLMSyncInterval = d
	}

	return cfg, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
