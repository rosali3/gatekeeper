// Command testupstream is a minimal HTTP backend used behind gatekeeper in
// tests and the docker-compose demo. Delay and error rate are configurable
// through environment variables so a single binary can simulate a slow or
// flaky upstream without extra flags.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run holds the defers (signal-context cancellation, shutdown-context
// cancellation), so any exit path is a plain return - never log.Fatal/
// os.Exit while those are pending, or they'd be skipped.
func run() error {
	addr := getEnv("ADDR", ":9000")
	name := getEnv("NAME", "test-upstream")
	delay := getEnvDuration("DELAY", 0)
	errorRate := getEnvFloat("ERROR_RATE", 0)
	healthy := getEnvBool("HEALTHY", true)

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(name, delay, errorRate, healthy),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("%s listening on %s (delay=%s error_rate=%v healthy=%v)", name, addr, delay, errorRate, healthy)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

// newMux builds the test upstream's handler. healthy is a snapshot taken at
// startup (from the HEALTHY env var) - this binary has no runtime control
// endpoint, per spec.
func newMux(name string, delay time.Duration, errorRate float64, healthy bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy || (errorRate > 0 && rand.Float64() < errorRate) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		if errorRate > 0 && rand.Float64() < errorRate {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"upstream": name,
			"method":   r.Method,
			"path":     r.URL.Path,
		})
	})
	return mux
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, v, err)
	}
	return d
}

func getEnvFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, v, err)
	}
	return f
}

func getEnvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, v, err)
	}
	return b
}
