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

	ctx := context.Background()
	rbac := control.NewPostgresRBACStore(db)

	// 内置角色与权限集以 Go 注册表为准，每次启动整体同步一次。
	// 放在 CheckBootstrapConfig 之前——后者要数 platform_admin 授予，
	// 依赖角色行已经存在。
	if err := rbac.SyncBuiltinRoles(ctx); err != nil {
		return err
	}
	if err := rbac.ValidatePermissions(ctx); err != nil {
		return err
	}

	// 没有任何管理员且没配 bootstrap 时拒绝启动——
	// 不允许出现「谁都能登、登进去就是管理员」的窗口期。
	if err := control.CheckBootstrapConfig(ctx, rbac, cfg.BootstrapAdmin); err != nil {
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
		RBAC:           rbac,
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

	// 对账循环（Task 22）用这个可取消 context；收到退出信号时调用
	// stopReconciler（见下方 select 的 <-stop 分支），runCtx 随之取消，
	// reconciler.Run 的循环观察到 ctx.Done() 后退出。
	runCtx, stopReconciler := context.WithCancel(ctx)
	defer stopReconciler()

	if os.Getenv("LDAP_URL") != "" {
		reconciler := control.NewReconciler(control.ReconcilerDeps{
			Users:    users,
			Sessions: sessions,
			Identity: control.NewLDAPIdentitySource(control.LDAPConfig{
				URL:      os.Getenv("LDAP_URL"),
				BindDN:   os.Getenv("LDAP_BIND_DN"),
				BindPass: os.Getenv("LDAP_BIND_PASSWORD"),
				BaseDN:   os.Getenv("LDAP_BASE_DN"),
			}),
			Keys: control.NewPostgresKeyRevoker(db),
		})
		go reconciler.Run(runCtx, cfg.ReconcileInterval)
		slog.Info("离职对账已启用", "interval", cfg.ReconcileInterval)
	} else {
		slog.Warn("未配置 LDAP_URL，离职对账未启用")
	}

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
