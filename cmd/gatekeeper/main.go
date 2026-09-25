// Command gatekeeper is the API gateway entry point: loads config, builds
// the routing/proxy pipeline and serves it with a graceful shutdown. The
// admin API and hot reload land in a later stage.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gatekeeper/internal/config"
	"gatekeeper/internal/gateway"
	"gatekeeper/internal/logger"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// shutdown signal arrives, before the server forcibly closes them. Not
// config-exposed: the TZ's YAML schema has no field for it, so it's a fixed
// implementation default like the transport/idle timeouts in
// internal/gateway.
const shutdownTimeout = 15 * time.Second

// idleTimeout bounds how long a keep-alive client connection may sit idle
// between requests. Same rationale as shutdownTimeout: not in the schema,
// so it's a sensible hardcoded default.
const idleTimeout = 120 * time.Second

func main() {
	configPath := flag.String("config", "configs/gatekeeper.example.yaml", "path to the gatekeeper config file")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	flag.Parse()

	log := logger.New(os.Stdout, *logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("failed to load config", "error", err, "path", *configPath)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		log.Error("failed to listen", "error", err, "listen", cfg.Server.Listen)
		os.Exit(1)
	}

	if err := serve(ln, cfg, log); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// serve owns the signal-context's lifetime, so its defer runs before main
// can os.Exit on error.
func serve(ln net.Listener, cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, ln, cfg, log)
}

// run serves the gateway on ln until ctx is canceled, then drains in-flight
// requests up to shutdownTimeout. Taking ln (rather than binding inside)
// lets tests use an ephemeral port and know its address up front.
func run(ctx context.Context, ln net.Listener, cfg *config.Config, log *slog.Logger) error {
	snap, err := gateway.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	gw := gateway.New()
	gw.Swap(snap)

	srv := &http.Server{
		Handler:           gw,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration(),
		IdleTimeout:       idleTimeout,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("gatekeeper listening",
			"listen", ln.Addr().String(),
			"upstreams", len(cfg.Upstreams),
			"routes", len(cfg.Routes),
		)
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received, draining in-flight requests", "timeout", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
