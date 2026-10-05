package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"novel-bot/internal/auth"
	"novel-bot/internal/browser"
	"novel-bot/internal/config"
	"novel-bot/internal/httpapi"
	"novel-bot/internal/sessions"
	"novel-bot/internal/storage/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := postgres.Open(startup, cfg.DatabaseURL, cfg.DBMaxConns)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	repository := postgres.NewRepository(pool)
	service, err := auth.NewService(repository, cfg.APIKeyPepper)
	if err != nil {
		return err
	}
	accounts, err := auth.NewAccountService(repository, cfg.APIKeyPepper, cfg.SessionTTL)
	if err != nil {
		return err
	}
	ready := repository.Ready
	startup, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = ready(startup)
	cancel()
	if err != nil {
		return fmt.Errorf("authentication schema unavailable; run make migrate: %w", err)
	}
	launcher, err := browser.NewChromium(cfg.ChromiumPath, cfg.BrowserProfileDir)
	if err != nil {
		return err
	}
	manager, err := sessions.NewManager(launcher, sessions.Options{MaxSessions: cfg.BrowserMaxSessions, TTL: cfg.BrowserSessionTTL, StartupTimeout: cfg.BrowserStartupTimeout})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Close(cleanup); err != nil {
			logger.Error("browser cleanup failed", "error", err)
		}
	}()
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(service, accounts, ready, logger, cfg.AuthTimeout, httpapi.RouterOptions{Sessions: manager, PublicAPIURL: cfg.PublicAPIURL}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.BrowserStartupTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	logger.Info("API listening", "address", cfg.HTTPAddr)
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Shutdown does not close hijacked WebSockets; stopping browsers does.
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		closeErr := manager.Close(cleanup)
		cancel()
		if closeErr != nil {
			logger.Error("browser cleanup failed", "error", closeErr)
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
