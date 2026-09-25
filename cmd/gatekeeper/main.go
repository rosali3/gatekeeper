// Command gatekeeper is the API gateway entry point. At this stage it only
// loads and validates configuration and sets up logging; routing, the
// proxy and the admin API are added in later stages.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"gatekeeper/internal/config"
	"gatekeeper/internal/logger"
)

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
	log.Info("config loaded",
		"config_path", *configPath,
		"listen", cfg.Server.Listen,
		"admin_listen", cfg.Admin.Listen,
		"upstreams", len(cfg.Upstreams),
		"routes", len(cfg.Routes),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("gatekeeper skeleton is up; HTTP server and proxying land in the next stage")
	<-ctx.Done()
	log.Info("shutdown signal received")
}
