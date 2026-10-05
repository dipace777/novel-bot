package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"novel-bot/internal/auth"
	"novel-bot/internal/config"
	"novel-bot/internal/httpapi"
	"novel-bot/internal/sessions"
	"novel-bot/internal/storage/postgres"
	redisstore "novel-bot/internal/storage/redis"
	"novel-bot/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadAPI()
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
	startup, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = repository.Ready(startup)
	cancel()
	if err != nil {
		return fmt.Errorf("authentication schema unavailable; run make migrate: %w", err)
	}
	startup, cancel = context.WithTimeout(ctx, 5*time.Second)
	redis, err := redisstore.Open(startup, cfg.RedisURL)
	cancel()
	if err != nil {
		return err
	}
	defer redis.Close()
	directory, err := redisstore.NewDirectory(redis, cfg.RedisNamespace)
	if err != nil {
		return err
	}
	publicListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer publicListener.Close()
	client := worker.NewClient(cfg.WorkerAuthToken)
	defer client.Close()
	cluster := sessions.NewCluster(directory, client, cfg.SessionStartupTimeout, repository)
	ready := func(ctx context.Context) error {
		if err := repository.Ready(ctx); err != nil {
			return err
		}
		return directory.Ping(ctx)
	}
	serving, stopServing := context.WithCancel(context.Background())
	defer stopServing()

	router := httpapi.NewRouter(service, accounts, ready, logger, cfg.AuthTimeout, httpapi.RouterOptions{
		Sessions:              cluster,
		PublicAPIURL:          cfg.PublicAPIURL,
		WorkerAuthToken:       cfg.WorkerAuthToken,
		RateLimiter:           directory,
		AuthRequestsPerMinute: cfg.AuthRequestsPerMinute,
		TrustedProxies:        cfg.TrustedProxies,
	})
	public := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.SessionStartupTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		BaseContext:       func(net.Listener) context.Context { return serving },
	}
	stopped := make(chan error, 1)
	go func() { stopped <- public.Serve(publicListener) }()
	logger.Info("API listening", "api_address", cfg.HTTPAddr)
	var result error
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	case <-ctx.Done():
	}
	// Cancel HTTP and hijacked CDP proxy contexts; browsers stay on their workers.
	stopServing()
	cleanup, finish := context.WithTimeout(context.Background(), 15*time.Second)
	defer finish()
	if err := public.Shutdown(cleanup); err != nil {
		_ = public.Close()
		if result == nil {
			result = err
		}
	}
	return result
}
