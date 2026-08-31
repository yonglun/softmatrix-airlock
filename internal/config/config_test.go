package config

import (
	"testing"
	"time"

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

func TestLoadControlDefaults(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	require.NoError(t, err)
	require.Equal(t, ":8081", cfg.ControlListenAddr)
	require.Equal(t, 5*time.Minute, cfg.ReconcileInterval)
}

func TestLoadControlEnv(t *testing.T) {
	env := map[string]string{
		"CONTROL_LISTEN_ADDR":     ":9091",
		"OIDC_ISSUER":             "http://casdoor:8000",
		"OIDC_CLIENT_ID":          "airlock",
		"OIDC_CLIENT_SECRET":      "s3cret",
		"OIDC_REDIRECT_URL":       "http://localhost:8081/auth/callback",
		"AIRLOCK_BOOTSTRAP_ADMIN": "admin@example.com",
		"RECONCILE_INTERVAL":      "30s",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, ":9091", cfg.ControlListenAddr)
	require.Equal(t, "http://casdoor:8000", cfg.OIDCIssuer)
	require.Equal(t, "airlock", cfg.OIDCClientID)
	require.Equal(t, "s3cret", cfg.OIDCClientSecret)
	require.Equal(t, "http://localhost:8081/auth/callback", cfg.OIDCRedirectURL)
	require.Equal(t, "admin@example.com", cfg.BootstrapAdmin)
	require.Equal(t, 30*time.Second, cfg.ReconcileInterval)
}

func TestLoadRejectsBadReconcileInterval(t *testing.T) {
	env := map[string]string{"RECONCILE_INTERVAL": "5 minutes"}
	_, err := Load(func(k string) string { return env[k] })
	require.Error(t, err)
	require.Contains(t, err.Error(), "RECONCILE_INTERVAL")
}

func TestReconcileIntervalRejectsNonPositive(t *testing.T) {
	// 复审第 5 条：time.NewTicker 在 d <= 0 时 panic，
	// 且 panic 发生在无 recover 的 goroutine 里，整个进程崩溃。
	// RECONCILE_INTERVAL=0 是「关掉对账」的自然写法（实际做法是留空 LDAP_URL），
	// 必须给出明确的配置错误而不是崩溃。
	for _, raw := range []string{"0", "0s", "-5m"} {
		_, err := Load(func(k string) string {
			if k == "RECONCILE_INTERVAL" {
				return raw
			}
			return ""
		})
		require.Error(t, err, "RECONCILE_INTERVAL=%s 应被拒绝", raw)
		require.Contains(t, err.Error(), "RECONCILE_INTERVAL")
	}
}

func TestReconcileIntervalAcceptsPositive(t *testing.T) {
	cfg, err := Load(func(k string) string {
		if k == "RECONCILE_INTERVAL" {
			return "30s"
		}
		return ""
	})
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.ReconcileInterval)
}
