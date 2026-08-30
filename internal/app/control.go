package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/softmatrix/airlock/internal/config"
	"github.com/softmatrix/airlock/internal/control"
	"github.com/softmatrix/airlock/migrations"
)

// RunControl 启动管理面进程。
func RunControl() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		return err
	}

	users := control.NewPostgresUserStore(db)
	sessions := control.NewPostgresSessionStore(db)
	loginStates := control.NewPostgresLoginStateStore(db)
	orgs := control.NewPostgresOrgStore(db)

	// 没有任何管理员且没配 bootstrap 时拒绝启动——
	// 不允许出现「谁都能登、登进去就是管理员」的窗口期。
	ctx := context.Background()
	if err := control.CheckBootstrapConfig(ctx, users, cfg.BootstrapAdmin); err != nil {
		return err
	}

	oidcClient, err := control.NewOIDCClient(ctx, control.OIDCConfig{
		Issuer:       cfg.OIDCIssuer,
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
	})
	if err != nil {
		return err
	}

	auth := control.NewAuth(control.AuthDeps{
		Users:          users,
		Sessions:       sessions,
		LoginStates:    loginStates,
		OIDC:           oidcClient,
		BootstrapAdmin: cfg.BootstrapAdmin,
		SecureCookie:   strings.HasPrefix(cfg.OIDCRedirectURL, "https://"),
	})

	ldapSource := control.NewLDAPSource(control.LDAPConfig{
		URL:      os.Getenv("LDAP_URL"),
		BindDN:   os.Getenv("LDAP_BIND_DN"),
		BindPass: os.Getenv("LDAP_BIND_PASSWORD"),
		BaseDN:   os.Getenv("LDAP_BASE_DN"),
	})

	srv := &http.Server{
		Addr: cfg.ControlListenAddr,
		Handler: control.NewServer(control.ServerDeps{
			Auth:   auth,
			OrgAPI: control.NewOrgAPI(orgs, ldapSource),
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 对账循环（Task 22）会用到这个可取消 context；
	// 这里先保留 stopReconciler 以便退出时统一收口，
	// 但 spec 给的实现从未消费 WithCancel 返回的 context 本身，
	// 会被 go vet 判定为未使用变量，因此用 _ 丢弃（详见任务报告中的偏差说明）。
	_, stopReconciler := context.WithCancel(ctx)
	defer stopReconciler()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("Control 启动",
			"addr", cfg.ControlListenAddr, "issuer", cfg.OIDCIssuer,
			"reconcile_interval", cfg.ReconcileInterval)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		slog.Info("收到退出信号，开始优雅关闭")
		stopReconciler()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
