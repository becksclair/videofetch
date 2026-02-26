package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"videofetch/internal/config"
	"videofetch/internal/download"
	"videofetch/internal/logging"
	"videofetch/internal/server"
	"videofetch/internal/store"
)

func main() {
	// Create config with defaults
	cfg := config.New()

	// Parse flags into config
	flag.StringVar(&cfg.OutputDir, "output-dir", "", "Directory for downloaded videos (default: $HOME/Videos/videofetch)")
	flag.IntVar(&cfg.Port, "port", cfg.Port, "Server port")
	flag.StringVar(&cfg.Host, "host", cfg.Host, "Host address to bind")
	flag.IntVar(&cfg.Workers, "workers", cfg.Workers, "Number of concurrent download workers")
	flag.IntVar(&cfg.QueueCap, "queue", cfg.QueueCap, "Download queue capacity")
	flag.StringVar(&cfg.DBPath, "db", "", "Path to SQLite database (default: OS cache dir: videofetch/videofetch.db)")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level: debug, info, warn, error")
	flag.BoolVar(&cfg.UnsafeLogPayloads, "unsafe-log-payloads", cfg.UnsafeLogPayloads, "Enable unsafe raw API payload logging (may leak secrets)")
	flag.Parse()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Initialize structured logging with configured level
	logging.Init(logging.ParseLevel(cfg.LogLevel))

	// Resolve paths
	if err := cfg.ResolveOutputDir(); err != nil {
		slog.Error("failed to resolve output directory", "error", err)
		os.Exit(1)
	}
	if err := cfg.ResolveDBPath(); err != nil {
		slog.Error("failed to resolve database path", "error", err)
		os.Exit(1)
	}

	// Create output directory
	if err := os.MkdirAll(cfg.AbsOutputDir, 0o755); err != nil {
		slog.Error("failed to create output directory", "path", cfg.AbsOutputDir, "error", err)
		os.Exit(1)
	}

	// Check yt-dlp presence early
	if err := download.CheckYTDLP(); err != nil {
		slog.Error("yt-dlp not found", "error", err)
		os.Exit(1)
	}

	// Ensure DB directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.AbsDBPath), 0o755); err != nil {
		slog.Error("failed to create database directory", "path", filepath.Dir(cfg.AbsDBPath), "error", err)
		os.Exit(1)
	}

	// Open database
	st, err := store.Open(cfg.AbsDBPath)
	if err != nil {
		slog.Error("failed to open database", "path", cfg.AbsDBPath, "error", err)
		os.Exit(1)
	}
	// Note: st.Close() is now called explicitly during shutdown

	// Create download manager with config
	mgr := download.NewManager(cfg.AbsOutputDir, cfg.Workers, cfg.QueueCap)
	mgr.SetStore(st)
	defer mgr.Shutdown()

	// Start database worker to process pending URLs
	dbWorker := download.NewDBWorker(st, mgr)

	// Retry any incomplete downloads from previous sessions
	if err := dbWorker.RetryIncompleteDownloads(); err != nil {
		slog.Warn("startup retry failed", "error", err)
	}

	dbWorker.Start()
	defer dbWorker.Stop()

	// Create HTTP server
	mux := server.New(mgr, st, cfg.AbsOutputDir, server.Options{
		UnsafeLogPayloads: cfg.UnsafeLogPayloads,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // allow streaming progress without premature timeouts
		IdleTimeout:       60 * time.Second,
	}

	// Start server
	go func() {
		// Log server start with config summary
		logging.LogServerStart(cfg.Addr, cfg.Summary())
		slog.Debug("configuration details", "config", cfg.String())

		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-shutdownCtx.Done()
	slog.Info("shutdown signal received", "event", "shutdown_start")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Stop taking new jobs and cancel in-flight
	mgr.StopAccepting()
	if err := srv.Shutdown(ctx); err != nil {
		logging.LogServerShutdown("http shutdown error", err)
	}
	mgr.Shutdown()
	// Close store after manager shutdown to avoid race conditions
	st.Close()
	logging.LogServerShutdown("shutdown complete", nil)
}
