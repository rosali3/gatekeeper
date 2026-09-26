// Command gatekeeper is the API gateway entry point: loads config, builds
// the routing/proxy pipeline, serves it with a graceful shutdown, and
// exposes a separate Bearer-token-protected admin API. Config reloads on
// SIGHUP, on the config file changing on disk, or via POST
// /admin/reload.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gatekeeper/internal/admin"
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

// adminReadHeaderTimeout is the admin server's own header-read timeout -
// not config-exposed, same rationale as above; the main server's comes
// from server.read_header_timeout since that one is in the schema.
const adminReadHeaderTimeout = 5 * time.Second

// metricsRefreshInterval is how often gateway_upstream_healthy/
// gateway_circuit_state get polled and re-exported - see
// gateway.Gateway.RefreshMetrics's doc comment for why those two need
// polling instead of being recorded at request time like the others.
const metricsRefreshInterval = 5 * time.Second

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

	mainLn, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		log.Error("failed to listen", "error", err, "listen", cfg.Server.Listen)
		os.Exit(1)
	}
	adminLn, err := net.Listen("tcp", cfg.Admin.Listen)
	if err != nil {
		log.Error("failed to listen (admin)", "error", err, "listen", cfg.Admin.Listen)
		os.Exit(1)
	}

	adminToken := os.Getenv(cfg.Admin.TokenEnv)
	if adminToken == "" {
		log.Error("admin token env var is unset or empty - refusing to start an unprotected admin API", "env", cfg.Admin.TokenEnv)
		os.Exit(1)
	}

	if err := serve(mainLn, adminLn, cfg, *configPath, adminToken, log); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// serve owns both the tracing-provider's and the signal-context's
// lifetimes, so their defers run before main can os.Exit on error.
func serve(mainLn, adminLn net.Listener, cfg *config.Config, configPath, adminToken string, log *slog.Logger) error {
	tp, err := setupTracing()
	if err != nil {
		return fmt.Errorf("setting up tracing: %w", err)
	}
	defer shutdownTracing(context.Background(), tp)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, mainLn, adminLn, cfg, configPath, adminToken, log)
}

// run serves the gateway and admin API on their respective listeners
// until ctx is canceled, then drains in-flight requests up to
// shutdownTimeout. Taking the listeners (rather than binding inside)
// lets tests use ephemeral ports and know their addresses up front.
func run(ctx context.Context, mainLn, adminLn net.Listener, cfg *config.Config, configPath, adminToken string, log *slog.Logger) error {
	snap, err := gateway.Build(ctx, cfg, log, nil)
	if err != nil {
		return err
	}
	gw := gateway.New()
	gw.Swap(snap)

	reload := newReloader(ctx, gw, configPath, log)

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)

	fileChanged := make(chan struct{}, 1)
	go watchConfigFile(ctx, configPath, fileChanged, log)

	go refreshMetricsPeriodically(ctx, gw, metricsRefreshInterval)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sighup:
				log.Info("SIGHUP received, reloading config")
				_ = reload()
			case <-fileChanged:
				log.Info("config file changed on disk, reloading")
				_ = reload()
			}
		}
	}()

	mainSrv := &http.Server{
		Handler:           gw,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Duration(),
		IdleTimeout:       idleTimeout,
	}
	adminSrv := &http.Server{
		Handler:           admin.NewHandler(gw, reload, adminToken),
		ReadHeaderTimeout: adminReadHeaderTimeout,
	}

	serveErr := make(chan error, 2)
	go func() {
		log.Info("gatekeeper listening", "listen", mainLn.Addr().String(), "upstreams", len(cfg.Upstreams), "routes", len(cfg.Routes))
		serveErr <- mainSrv.Serve(mainLn)
	}()
	go func() {
		log.Info("admin API listening", "listen", adminLn.Addr().String())
		serveErr <- adminSrv.Serve(adminLn)
	}()

	select {
	case err := <-serveErr:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = mainSrv.Shutdown(shutdownCtx)
		_ = adminSrv.Shutdown(shutdownCtx)
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining in-flight requests", "timeout", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		var errs []error
		if err := mainSrv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		if err := adminSrv.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
}

// refreshMetricsPeriodically polls gw.RefreshMetrics until ctx is
// canceled, keeping gateway_upstream_healthy/gateway_circuit_state
// current even during a quiet period with no traffic to derive them from.
func refreshMetricsPeriodically(ctx context.Context, gw *gateway.Gateway, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gw.RefreshMetrics()
		}
	}
}

// newReloader returns the function that actually performs a reload:
// re-read and validate configPath, rebuild the gateway (reusing state
// for anything unchanged, per gateway.Build), and swap it in. On any
// error the current config keeps running - the old Snapshot is simply
// never replaced - and the error is both logged and returned so the
// admin API's POST /admin/reload can report it too.
func newReloader(ctx context.Context, gw *gateway.Gateway, configPath string, log *slog.Logger) func() error {
	return func() error {
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Error("reload: failed to load config, keeping current config", "error", err)
			return err
		}
		snap, err := gateway.Build(ctx, cfg, log, gw.Current())
		if err != nil {
			log.Error("reload: failed to build gateway, keeping current config", "error", err)
			return err
		}
		gw.Swap(snap)
		log.Info("config reloaded", "upstreams", len(cfg.Upstreams), "routes", len(cfg.Routes))
		return nil
	}
}
