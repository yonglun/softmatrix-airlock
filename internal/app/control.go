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

	"github.com/softmatrix/airlock/internal/authz"
	"github.com/softmatrix/airlock/internal/config"
	"github.com/softmatrix/airlock/internal/control"
	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/litellm"
	"github.com/softmatrix/airlock/internal/notify"
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

	resolver := authz.NewResolver(rbac)

	// 同一个 LiteLLM 管理客户端在整个进程里复用——同步、签发、审批 worker
	// 与离职对账都要调它，没有理由各建一份。
	litellmAdmin := litellm.New(litellm.Config{
		BaseURL:   cfg.LiteLLMBaseURL,
		MasterKey: cfg.LiteLLMMasterKey,
	})

	// LiteLLM 同步。未配置 master key 时 syncer 为 nil，
	// OrgAPI 的 Nudge 与 DropNode 随之变成 no-op，状态接口回答「未启用」。
	var syncer *control.Syncer
	if cfg.LiteLLMMasterKey != "" {
		syncer = control.NewSyncer(control.SyncerDeps{
			Orgs:  orgs,
			Admin: litellmAdmin,
		})
	}

	// 签发必须加密上游密钥。启动时响亮失败，而不是等第一次签发才 500——
	// 与 CheckBootstrapConfig 的哲学一致。
	if len(cfg.EncryptionKey) == 0 {
		return errors.New("未配置 AIRLOCK_ENCRYPTION_KEY，无法签发密钥")
	}
	cipher, err := cryptobox.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	keyStore := control.NewPostgresKeyStore(db)
	issuer := control.NewKeyIssuer(control.KeyIssuerDeps{
		Keys: keyStore, Orgs: orgs, Cipher: cipher, Admin: litellmAdmin,
	})

	requests := control.NewPostgresRequestStore(db)
	notifs := control.NewPostgresNotificationStore(db)

	approval := control.NewApprovalService(control.ApprovalDeps{
		Requests: requests, Notifs: notifs, Keys: keyStore,
		Orgs: orgs, RBAC: rbac, Users: users,
		Issuer: issuer, Resolver: resolver,
	})
	approvalWorker := control.NewApprovalWorker(control.ApprovalWorkerDeps{
		Requests: requests, Notifs: notifs, Keys: keyStore, Users: users,
		Admin:  litellmAdmin,
		Cipher: cipher,
		Sender: notify.NewSMTPSender(notify.SMTPConfig{
			Addr: cfg.SMTPAddr, From: cfg.SMTPFrom,
		}),
	})

	srv := &http.Server{
		Addr: cfg.ControlListenAddr,
		Handler: control.NewServer(control.ServerDeps{
			Auth:       auth,
			OrgAPI:     control.NewOrgAPI(orgs, ldapSource, resolver).WithNudger(syncer),
			GrantAPI:   control.NewGrantAPI(users, rbac, resolver),
			SyncAPI:    control.NewSyncAPI(syncer),
			KeyAPI:     control.NewKeyAPI(issuer, keyStore, orgs, resolver),
			RequestAPI: control.NewRequestAPI(approval, requests, approvalWorker),
			Resolver:   resolver,
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
			Keys: control.NewPostgresKeyRevoker(db, litellmAdmin, cipher),
		})
		go reconciler.Run(runCtx, cfg.ReconcileInterval)
		slog.Info("离职对账已启用", "interval", cfg.ReconcileInterval)
	} else {
		slog.Warn("未配置 LDAP_URL，离职对账未启用")
	}

	if syncer != nil {
		go syncer.Run(runCtx, cfg.LiteLLMSyncInterval)
		slog.Info("LiteLLM 同步已启用",
			"base_url", cfg.LiteLLMBaseURL, "interval", cfg.LiteLLMSyncInterval)
	} else {
		slog.Warn("未配置 LITELLM_MASTER_KEY，LiteLLM 同步未启用")
	}

	go approvalWorker.Run(runCtx, cfg.ApprovalWorkerInterval)
	slog.Info("审批 worker 已启动",
		"interval", cfg.ApprovalWorkerInterval, "smtp", cfg.SMTPAddr)

	// 滞留的 pending 是「上游调用与 MarkActive 之间崩掉」留下的残骸。
	// 阈值取 10 分钟：远大于一次签发的正常耗时，不会误伤进行中的签发。
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			n, err := issuer.CleanupStalePending(runCtx, 10*time.Minute)
			if err != nil {
				slog.Error("清理滞留密钥失败", "err", err)
				continue
			}
			if n > 0 {
				slog.Warn("已清理滞留的待建密钥", "count", n)
			}
		}
	}()

	// 密钥维护循环：补做上游封禁 + 清理过期的旧凭据。
	//
	// 上游封禁是兜底——批量吊销只做本地 UPDATE 就返回（上游没有批量封禁
	// 接口），单把吊销与离职对账内联失败时也留白给这里收敛。
	// 清理过期旧凭据纯属卫生：到期判断在 Edge 的 SQL 里，
	// 这个循环从不运行也不会留下一把还能用的旧密钥。
	go func() {
		ticker := time.NewTicker(cfg.KeyMaintenanceInterval)
		defer ticker.Stop()
		for {
			if n, err := issuer.BlockPendingUpstream(runCtx); err != nil {
				slog.Error("补做上游封禁失败，将在下轮重试", "err", err)
			} else if n > 0 {
				slog.Info("已补做上游封禁", "count", n)
			}
			if n, err := keyStore.RetireExpiredPrevKeys(runCtx, time.Now()); err != nil {
				slog.Error("清理过期旧凭据失败，将在下轮重试", "err", err)
			} else if n > 0 {
				slog.Info("已清理过期的轮换旧凭据", "count", n)
			}

			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	slog.Info("密钥维护循环已启动", "interval", cfg.KeyMaintenanceInterval)

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
