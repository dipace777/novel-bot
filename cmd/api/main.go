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
	"novel-bot/internal/browser"
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
	launcher, err := browser.NewChromium(cfg.ChromiumPath, cfg.BrowserProfileDir)
	if err != nil {
		return err
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID, err = sessions.NewID()
		if err != nil {
			return err
		}
	}

	// Bind both ports before publishing this worker in the directory.
	privateListener, err := net.Listen("tcp", cfg.WorkerHTTPAddr)
	if err != nil {
		return fmt.Errorf("listen for worker traffic: %w", err)
	}
	defer privateListener.Close()
	publicListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer publicListener.Close()
	startup, cancel = context.WithTimeout(ctx, 5*time.Second)
	agent, err := worker.NewAgent(startup, directory, launcher, sessions.Worker{ID: cfg.WorkerID, URL: cfg.WorkerURL}, sessions.Options{MaxSessions: cfg.BrowserMaxSessions, TTL: cfg.BrowserSessionTTL, StartupTimeout: cfg.BrowserStartupTimeout}, cfg.WorkerLeaseTTL, logger)
	cancel()
	if err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agent.Close(cleanup); err != nil {
			logger.Error("worker cleanup failed")
		}
	}()
	client := worker.NewClient(cfg.WorkerAuthToken)
	defer client.Close()
	cluster := sessions.NewCluster(directory, client, cfg.BrowserStartupTimeout, repository)
	ready := func(ctx context.Context) error {
		if err := repository.Ready(ctx); err != nil {
			return err
		}
		if err := directory.CheckWorker(ctx, agent.Worker()); err != nil {
			return err
		}
		return agent.Ready()
	}
	public := &http.Server{Handler: httpapi.NewRouter(service, accounts, ready, logger, cfg.AuthTimeout, httpapi.RouterOptions{Sessions: cluster, PublicAPIURL: cfg.PublicAPIURL, WorkerAuthToken: cfg.WorkerAuthToken, RateLimiter: directory, AuthRequestsPerMinute: cfg.AuthRequestsPerMinute, TrustedProxies: cfg.TrustedProxies}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: cfg.BrowserStartupTimeout + 10*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	private := &http.Server{Handler: httpapi.NewWorkerRouter(agent, cfg.WorkerAuthToken, logger), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: cfg.BrowserStartupTimeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	stopped := make(chan error, 2)
	go func() { stopped <- public.Serve(publicListener) }()
	go func() { stopped <- private.Serve(privateListener) }()
	logger.Info("API and browser worker listening", "api_address", cfg.HTTPAddr, "worker_address", cfg.WorkerHTTPAddr, "worker_id", cfg.WorkerID)
	var result error
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	case <-agent.Done():
		result = sessions.ErrLeaseLost
	case <-ctx.Done():
	}
	// Closing the worker terminates Chromium and the hijacked WebSockets before
	// graceful HTTP shutdown (which does not close hijacked connections itself).
	cleanup, finish := context.WithTimeout(context.Background(), 15*time.Second)
	defer finish()
	if err := agent.Close(cleanup); err != nil && result == nil {
		result = err
	}
	for _, server := range []*http.Server{public, private} {
		if err := server.Shutdown(cleanup); err != nil {
			_ = server.Close()
			if result == nil {
				result = err
			}
		}
	}
	return result
}
