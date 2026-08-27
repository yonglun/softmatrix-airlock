// Package config 从环境变量加载 Airlock 的运行配置。
// 不依赖任何第三方库，便于在测试中注入取值函数。
package config

import (
	"encoding/base64"
	"fmt"
)

// Getenv 是取环境变量的函数，测试可注入内存实现。
type Getenv func(key string) string

type Config struct {
	EdgeListenAddr  string
	UpstreamBaseURL string
	PostgresDSN     string
	ClickHouseDSN   string
	EncryptionKey   []byte
}

const encryptionKeyLen = 32

func Load(getenv Getenv) (Config, error) {
	cfg := Config{
		EdgeListenAddr:  or(getenv("EDGE_LISTEN_ADDR"), ":8080"),
		UpstreamBaseURL: or(getenv("EDGE_UPSTREAM_BASE_URL"), "http://localhost:4000"),
		PostgresDSN:     getenv("POSTGRES_DSN"),
		ClickHouseDSN:   getenv("CLICKHOUSE_DSN"),
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

	return cfg, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
