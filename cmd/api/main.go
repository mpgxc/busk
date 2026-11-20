package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"comparai/internal/alerts"
	"comparai/internal/auth"
	"comparai/internal/catalog"
	"comparai/internal/config"
	"comparai/internal/db"
	"comparai/internal/httpapi"
	"comparai/internal/ingest"
	"comparai/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	alertSvc := alerts.NewService(pool, alerts.LogNotifier{})
	if os.Getenv("DISABLE_WORKER") != "true" {
		go alertSvc.Run(ctx, cfg.AlertsInterval, cfg.StaleOfferAfter)
	}

	srv := httpapi.New(cfg, pool, catalog.NewRepo(pool), ingest.NewService(pool),
		auth.NewService(pool, cfg.JWTSecret, cfg.JWTTTL), alertSvc, web.FS())

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(ctx),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
