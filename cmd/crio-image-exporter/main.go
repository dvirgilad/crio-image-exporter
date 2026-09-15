// Command crio-image-exporter exports CRI-O image disk metrics for Prometheus.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dvirgilad/crio-image-exporter/internal/agecache"
	"github.com/dvirgilad/crio-image-exporter/internal/collector"
	"github.com/dvirgilad/crio-image-exporter/internal/config"
	"github.com/dvirgilad/crio-image-exporter/internal/cri"
	"github.com/dvirgilad/crio-image-exporter/internal/storage"
)

// Injected at build time via -ldflags.
var (
	version  = "dev"
	revision = "unknown"
)

type readiness interface{ Ready() bool }

// probe tracks whether CRI has ever answered. Readiness is sticky: once the
// runtime has responded the pod stays ready, because a transient CRI hiccup
// should show up as scrape_success=0, not as the pod leaving the endpoints
// list and taking its metrics with it.
type probe struct{ ok atomic.Bool }

func (p *probe) Ready() bool { return p.ok.Load() }

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "crio-image-exporter: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.LookupEnv)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := cri.Dial(ctx, cfg.CRISocket, cfg.CRITimeout)
	if err != nil {
		return fmt.Errorf("connect to CRI: %w", err)
	}
	defer client.Close()

	opts := collector.Options{Version: version, Revision: revision}

	if cfg.CollectImageAge {
		cache := agecache.New(client, cfg.ImageAgeRefreshInterval, log)
		go cache.Run(ctx)
		opts.Age = cache
	}
	if cfg.StorageEnabled() {
		graph := storage.New(cfg.StorageRoot, cfg.StorageRefreshInterval, log)
		go graph.Run(ctx)
		opts.Storage = graph
		log.Info("storage inspection enabled", "root", cfg.StorageRoot)
	}

	p := &probe{}
	go watchReadiness(ctx, client, p, log)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collector.New(client, cfg, opts),
	)

	srv := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           newMux(cfg.MetricsPath, registry, p),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "address", cfg.ListenAddress, "metrics_path", cfg.MetricsPath, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// watchReadiness flips the probe once CRI answers. It polls only until the
// first success, since readiness is sticky.
func watchReadiness(ctx context.Context, client cri.Client, p *probe, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := client.Version(ctx); err == nil {
			p.ok.Store(true)
			log.Info("CRI connection established")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func newMux(metricsPath string, registry *prometheus.Registry, r readiness) *http.ServeMux {
	mux := http.NewServeMux()
	if registry != nil {
		mux.Handle(metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
			// A CRI failure must not turn into an HTTP error: the exporter
			// still has something to say, namely scrape_success=0.
			ErrorHandling: promhttp.ContinueOnError,
		}))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !r.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "CRI connection not yet established")
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
