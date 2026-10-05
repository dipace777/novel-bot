// fixtures serves repeatable HTTP pages for browser capacity measurements.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"novel-bot/internal/loadtest"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr := os.Getenv("FIXTURE_HTTP_ADDR")
	if addr == "" {
		addr = ":8082"
	}
	server := &http.Server{Addr: addr, Handler: loadtest.FixtureHandler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(cleanup)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("fixture server stopped", "error", err)
		os.Exit(1)
	}
}
