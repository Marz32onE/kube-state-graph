// Command kube-state-graph runs the multi-cluster pod / node graph API server.
//
//	@title			kube-state-graph API
//	@version		v1
//	@description	Multi-cluster pod / node / PVC graph API. Reads kube-state-metrics and pod-UID-resolved service-graph metrics from a centralised VictoriaMetrics and returns the joined cross-cluster graph as Cytoscape.js JSON or in the Grafana Node Graph datasource shape.
//	@license.name	Apache 2.0
//	@license.url	https://www.apache.org/licenses/LICENSE-2.0.html
//	@BasePath		/
//	@host			localhost:8080
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

	"github.com/marz32one/kube-state-graph/internal/api"
	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.LookupEnv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	logger.Info("starting kube-state-graph",
		"prom_url", cfg.PromURL,
		"listen_addr", cfg.ListenAddr,
		"max_window", cfg.MaxWindow,
		"max_pods", cfg.MaxPods,
		"clusters_allowlist", cfg.ClustersAllowlist,
		"external_name_pattern_set", cfg.ExternalNamePattern != "",
	)

	metrics := observability.NewMetrics()
	promClient, err := promql.New(cfg.PromURL, metrics)
	if err != nil {
		return fmt.Errorf("promql client: %w", err)
	}

	graphCache, err := cache.New(cfg.CacheMaxCostBytes, metrics)
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer graphCache.Close()

	builder := build.New(promClient, cfg, metrics)
	server := api.New(cfg, builder, graphCache, promClient, metrics, logger)

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		logger.Error("http server failed", "err", err)
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
