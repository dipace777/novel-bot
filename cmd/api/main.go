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
	"novel-bot/internal/config"
	"novel-bot/internal/httpapi"
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
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(service, accounts, ready, logger, cfg.AuthTimeout),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
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
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
