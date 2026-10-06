// worker runs Chromium and its private control API independently of the public API.
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

	"novel-bot/internal/browser"
	"novel-bot/internal/config"
	"novel-bot/internal/httpapi"
	"novel-bot/internal/observability"
	"novel-bot/internal/sessions"
	redisstore "novel-bot/internal/storage/redis"
	"novel-bot/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	launcher, err := browser.NewChromium(cfg.ChromiumPath, cfg.BrowserProfileDir, cfg.BrowserHeadless)
	if err != nil {
		return err
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID, err = sessions.NewID()
		if err != nil {
			return err
		}
	}
	// Bind before registration so a port collision cannot publish a dead worker.
	listener, err := net.Listen("tcp", cfg.WorkerHTTPAddr)
	if err != nil {
		return fmt.Errorf("listen for worker traffic: %w", err)
	}
	defer listener.Close()
	metrics := observability.New(cfg.WorkerID)
	startup, cancel = context.WithTimeout(ctx, 5*time.Second)
	agent, err := worker.NewAgent(startup, directory, launcher, sessions.Worker{ID: cfg.WorkerID, URL: cfg.WorkerURL}, sessions.Options{Observer: metrics, MaxSessions: cfg.BrowserMaxSessions, TTL: cfg.BrowserSessionTTL, StartupTimeout: cfg.BrowserStartupTimeout}, cfg.WorkerLeaseTTL, logger)
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
	metrics.Bind(agent)
	sampleCtx, stopSampling := context.WithCancel(context.Background())
	sampleDone := make(chan struct{})
	go func() { defer close(sampleDone); metrics.Sample(sampleCtx, launcher, cfg.MetricsSampleInterval) }()
	defer func() { stopSampling(); <-sampleDone }()
	ready := func(ctx context.Context) error {
		if err := agent.Ready(); err != nil {
			return err
		}
		if err := directory.CheckWorker(ctx, agent.Worker()); err != nil {
			return err
		}
		return agent.Ready()
	}
	server := &http.Server{
		Handler:           httpapi.NewWorkerRouter(agent, cfg.WorkerAuthToken, logger, httpapi.WorkerRouterOptions{Metrics: metrics.Handler(), Ready: ready}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.BrowserStartupTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	logger.Info("browser worker listening", "worker_address", cfg.WorkerHTTPAddr, "worker_id", cfg.WorkerID)
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
	// Retire the lease and stop browsers/hijacked CDP connections before HTTP drain.
	cleanup, finish := context.WithTimeout(context.Background(), 15*time.Second)
	defer finish()
	if err := agent.Close(cleanup); err != nil && result == nil {
		result = err
	}
	if err := server.Shutdown(cleanup); err != nil {
		_ = server.Close()
		if result == nil {
			result = err
		}
	}
	return result
}
