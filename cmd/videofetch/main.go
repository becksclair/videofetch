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
	"runtime"
	"syscall"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/server"
	"videofetch/internal/store"
)

func main() {
	var (
		outputDir string
		port      int
		host      string
		workers   int
		queueCap  int
		dbPath    string
	)

	flag.StringVar(&outputDir, "output-dir", "", "Directory for downloaded videos (required)")
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.StringVar(&host, "host", "0.0.0.0", "Host address to bind")
	flag.IntVar(&workers, "workers", 4, "Number of concurrent download workers")
	flag.IntVar(&queueCap, "queue", 128, "Download queue capacity")
	flag.StringVar(&dbPath, "db", "", "Path to SQLite database (default: OS cache dir: videofetch/videofetch.db)")
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

	// Open database (optional): default to OS cache directory
	if dbPath == "" {
		dbPath = defaultCacheDBPath()
	}
	// Ensure DB directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("create db dir: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	// Note: st.Close() is now called explicitly during shutdown

	// Hooks to persist progress/state
	hooks := &storeHooks{st: st}

	mgr := download.NewManager(absOut, workers, queueCap)
	mgr.SetHooks(hooks)
	defer mgr.Shutdown()
	
	// Start database worker to process pending URLs
	dbWorker := download.NewDBWorker(st, mgr)
	
	// Retry any incomplete downloads from previous sessions
	if err := dbWorker.RetryIncompleteDownloads(); err != nil {
		log.Printf("startup retry failed: %v", err)
	}
	
	dbWorker.Start()
	defer dbWorker.Stop()

	mux := server.New(mgr, st, absOut)

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
	// Close store after manager shutdown to avoid race conditions
	st.Close()
	log.Printf("shutdown complete")
}

// storeHooks implements download.Hooks to persist updates.
type storeHooks struct{ st *store.Store }

func (h *storeHooks) OnProgress(dbID int64, progress float64) {
	// Best-effort; log on failure but ignore database closure errors during shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.st.UpdateProgress(ctx, dbID, progress); err != nil {
		// Ignore database closure errors during shutdown and context cancellation
		if !h.isExpectedError(err) {
			log.Printf("db update progress id=%d: %v", dbID, err)
		}
	}
}

// defaultCacheDBPath returns the cross-platform default path for the SQLite DB
// as requested:
// - Windows: %APPDATA%/videofetch/videofetch.db
// - Linux/macOS: $HOME/.cache/videofetch/videofetch.db
func defaultCacheDBPath() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "videofetch", "videofetch.db")
		}
		// Fallback to user home if APPDATA is not set
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Roaming", "videofetch", "videofetch.db")
		}
		// Last resort: current directory
		return "videofetch.db"
	}
	// Linux/macOS default cache location
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "videofetch", "videofetch.db")
	}
	// Fallback: place in working directory
	return filepath.Join("videofetch", "videofetch.db")
}

func (h *storeHooks) OnStateChange(dbID int64, state download.State, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var st string
	switch state {
	case download.StateQueued:
		st = "pending"
	case download.StateDownloading:
		st = "downloading"
	case download.StateCompleted:
		st = "completed"
	case download.StateFailed:
		st = "error"
	default:
		st = "pending"
	}
	if err := h.st.UpdateStatus(ctx, dbID, st, errMsg); err != nil {
		// Ignore database closure errors during shutdown and context cancellation
		if !h.isExpectedError(err) {
			log.Printf("db update status id=%d: %v", dbID, err)
		}
	}
}

func (h *storeHooks) OnFilename(dbID int64, filename string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.st.UpdateFilename(ctx, dbID, filename); err != nil {
		// Ignore database closure errors during shutdown and context cancellation
		if !h.isExpectedError(err) {
			log.Printf("db update filename id=%d: %v", dbID, err)
		}
	}
}

// isExpectedError checks if an error is expected during shutdown or context cancellation
func (h *storeHooks) isExpectedError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return errStr == "sql: database is closed" ||
		errStr == "context deadline exceeded" ||
		errStr == "context canceled"
}
