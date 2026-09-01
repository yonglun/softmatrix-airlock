// Package app 装配 Airlock 各个进程角色。
// 每个角色一个文件，彼此不互相 import。
package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/config"
	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/edge"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
	"github.com/softmatrix/airlock/migrations"
)

// RunEdge 启动数据面进程。
func RunEdge() error {
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

	sink, err := usage.NewClickHouseSink(cfg.ClickHouseDSN)
	if err != nil {
		return err
	}
	defer sink.Close()

	writer := usage.NewBatchWriter(sink, 200, 2*time.Second)
	defer writer.Close()

	// 密钥改为每请求查库（P1.3a）。价格仍是内存态，留待后续接管。
	if len(cfg.EncryptionKey) == 0 {
		return errors.New("未配置 AIRLOCK_ENCRYPTION_KEY，无法解密上游密钥")
	}
	cipher, err := cryptobox.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	keys := apikey.NewPostgresStore(db, cipher)
	prices, err := pricing.NewMemoryTable(nil)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: cfg.EdgeListenAddr,
		Handler: edge.NewServer(edge.Deps{
			Keys:            keys,
			Prices:          prices,
			Usage:           writer,
			UpstreamBaseURL: cfg.UpstreamBaseURL,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("Edge 启动", "addr", cfg.EdgeListenAddr, "upstream", cfg.UpstreamBaseURL)
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}
