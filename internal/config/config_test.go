package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadUsesDefaultsWhenUnset(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.EdgeListenAddr)
	require.Equal(t, "http://localhost:4000", cfg.UpstreamBaseURL)
}

func TestLoadReadsEnv(t *testing.T) {
	env := map[string]string{
		"EDGE_LISTEN_ADDR":       ":9090",
		"EDGE_UPSTREAM_BASE_URL": "http://litellm:4000",
		"POSTGRES_DSN":           "postgres://u:p@h:5432/db",
		"CLICKHOUSE_DSN":         "clickhouse://u:p@h:9000/db",
		"AIRLOCK_ENCRYPTION_KEY": "YWlybG9jay1kZXYtb25seS0zMmJ5dGUta2V5ISEhISE=",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, ":9090", cfg.EdgeListenAddr)
	require.Equal(t, "http://litellm:4000", cfg.UpstreamBaseURL)
	require.Equal(t, "postgres://u:p@h:5432/db", cfg.PostgresDSN)
	require.Len(t, cfg.EncryptionKey, 32)
}

func TestLoadRejectsWrongLengthEncryptionKey(t *testing.T) {
	env := map[string]string{"AIRLOCK_ENCRYPTION_KEY": "c2hvcnQ="} // "short"
	_, err := Load(func(k string) string { return env[k] })
	require.Error(t, err)
	require.Contains(t, err.Error(), "32")
}
