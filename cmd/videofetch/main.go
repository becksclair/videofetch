package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/server"
)

func main() {
	var (
		outputDir        string
		port             int
		host             string
		workers          int
		queueCap         int
		ytdlpFormat      string
		ytdlpImpersonate string
	)

	flag.StringVar(&outputDir, "output-dir", "", "Directory for downloaded videos (required)")
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.StringVar(&host, "host", "0.0.0.0", "Host address to bind")
	flag.IntVar(&workers, "workers", 4, "Number of concurrent download workers")
	flag.IntVar(&queueCap, "queue", 128, "Download queue capacity")
	flag.StringVar(&ytdlpFormat, "yt-dlp-format", "bestvideo*+bestaudio/best", "yt-dlp format selector (-f). Overrides VIDEOFETCH_YTDLP_FORMAT if set.")
	flag.StringVar(&ytdlpImpersonate, "yt-dlp-impersonate", "", "yt-dlp --impersonate client (e.g., 'chrome' or 'chrome:windows-10'). Overrides VIDEOFETCH_YTDLP_IMPERSONATE if set.")
	flag.Parse()

	if outputDir == "" {
		log.Fatalf("--output-dir is required")
	}
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		log.Fatalf("resolve output dir: %v", err)
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	// Check yt-dlp presence early.
	if err := download.CheckYTDLP(); err != nil {
		log.Fatalf("yt-dlp not found: %v", err)
	}

	// CLI flags take precedence; if empty, Manager falls back to env and then defaults
	mgr := download.NewManagerWithOptions(absOut, workers, queueCap, download.ManagerOptions{Format: ytdlpFormat, Impersonate: ytdlpImpersonate})
	defer mgr.Shutdown()

	mux := server.New(mgr)

	addr := fmt.Sprintf("%s:%d", host, port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // allow streaming progress without premature timeouts
		IdleTimeout:       60 * time.Second,
	}

	// Start server
	go func() {
		log.Printf("videofetch listening on http://%s (out=%s, workers=%d, queue=%d)", addr, absOut, workers, queueCap)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-shutdownCtx.Done()
	log.Printf("shutdown signal received; draining...")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Stop taking new jobs and cancel in-flight
	mgr.StopAccepting()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	mgr.Shutdown()
	log.Printf("shutdown complete")
}
