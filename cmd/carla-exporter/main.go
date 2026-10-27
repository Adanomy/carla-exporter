package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/Adanomy/carla-exporter/internal/carla"
	"github.com/Adanomy/carla-exporter/internal/config"
	"github.com/Adanomy/carla-exporter/internal/exporter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var version = "dev"
var revision = "unknown"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "carla-exporter: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := config.Parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	build := exporter.BuildInfo{Version: version, Revision: revision, GoVersion: runtime.Version()}
	if build.Revision == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					build.Revision = setting.Value
				}
			}
		}
	}
	if cfg.Version {
		_, err := fmt.Fprintf(stdout, "carla-exporter %s revision=%s go=%s\n", build.Version, build.Revision, build.GoVersion)
		return err
	}
	level := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}[cfg.LogLevel]
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler = slog.NewTextHandler(stderr, options)
	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(stderr, options)
	}
	logger := slog.New(handler)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := carla.NewClient(cfg.CARLAAddress, cfg.MaxMessageBytes)
	defer client.Close()
	session := carla.NewSession(client, carla.SessionOptions{
		Address: cfg.CARLAAddress, StreamAddress: cfg.StreamAddress, Timeout: cfg.Timeout,
		PerformanceWindow: cfg.PerformanceWindow, ReconnectMin: cfg.ReconnectMin,
		ReconnectMax: cfg.ReconnectMax, MaxMessageBytes: cfg.MaxMessageBytes,
	}, logger)
	registry := prometheus.NewRegistry()
	collector := exporter.NewCollector(ctx, session, exporter.Options{Timeout: cfg.Timeout, ActorIDs: cfg.ActorIDs, Roles: cfg.Roles, Blueprints: cfg.Blueprints}, build, logger)
	registry.MustRegister(collector, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for HTTP: %w", err)
	}
	server := &http.Server{Handler: exporter.NewHandler(registry, cfg.Timeout), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: cfg.Timeout + 5*time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	var workers sync.WaitGroup
	workers.Go(func() { session.Run(ctx) })
	served := make(chan error, 1)
	workers.Go(func() { served <- server.Serve(listener) })
	logger.Info("CARLA exporter started", "listen_address", listener.Addr(), "carla_address", cfg.CARLAAddress, "version", build.Version, "performance_window", cfg.PerformanceWindow)
	select {
	case <-ctx.Done():
	case err = <-served:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		_ = server.Close()
		err = errors.Join(err, fmt.Errorf("shut down HTTP server: %w", shutdownErr))
	}
	workers.Wait()
	logger.Info("CARLA exporter stopped")
	return err
}
