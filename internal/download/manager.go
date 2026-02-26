package download

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"videofetch/internal/logging"
)

type State string

const (
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
)

const pendingMetadataTimeout = 30 * time.Second

var fetchMediaInfo = FetchMediaInfo

// progressData represents the JSON structure from yt-dlp's progress output
type progressData struct {
	Status             string  `json:"status"`
	DownloadedBytes    float64 `json:"downloaded_bytes"`
	TotalBytes         float64 `json:"total_bytes"`
	TotalBytesEstimate float64 `json:"total_bytes_estimate,omitempty"`
	Eta                float64 `json:"eta,omitempty"`
	Speed              float64 `json:"speed,omitempty"`
	FragmentIndex      int     `json:"fragment_index,omitempty"`
	FragmentCount      int     `json:"fragment_count,omitempty"`
}

type Item struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Progress float64 `json:"progress"` // 0-100
	State    State   `json:"state"`
	Error    string  `json:"error,omitempty"`

	// Optional metadata for UI convenience.
	Title        string `json:"title,omitempty"`
	Duration     int64  `json:"duration,omitempty"` // seconds
	ThumbnailURL string `json:"thumbnail_url,omitempty"`

	// Optional database binding for persistence updates.
	DBID int64 `json:"db_id,omitempty"`

	// Filename gets set when download is complete.
	Filename string `json:"filename,omitempty"`

	startedAt time.Time
	updatedAt time.Time
}

type job struct {
	id  string
	url string
}

type Manager struct {
	outDir string

	jobs    chan job
	wg      sync.WaitGroup
	closing atomic.Bool

	runCtx    context.Context
	runCancel context.CancelFunc

	// ensure Shutdown is safe to call multiple times
	shutdownOnce sync.Once

	// Track background goroutines for progress and status updates
	bgWg sync.WaitGroup

	// Refactored components
	registry   *ItemRegistry
	downloader *Downloader

	store Store

	workerDownload func(ctx context.Context, id, url string) error
}

// Store interface defines methods for persisting download state
type Store interface {
	UpdateProgress(ctx context.Context, id int64, progress float64) error
	UpdateStatus(ctx context.Context, id int64, status, errMsg string) error
	UpdateFilename(ctx context.Context, id int64, filename string) error
}

// PendingDownloadStore defines methods needed to process pending downloads from the database
type PendingDownloadStore interface {
	TryClaimPending(ctx context.Context, id int64) (bool, error)
	UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error
	UpdateMeta(ctx context.Context, id int64, title string, duration int64, thumbnail string) error
}

func NewManager(outputDir string, workers, queueCap int) *Manager {
	if workers <= 0 {
		workers = max(runtime.NumCPU(), 1)
	}
	if queueCap <= 0 {
		queueCap = 64
	}

	m := &Manager{
		outDir:     outputDir,
		jobs:       make(chan job, queueCap),
		registry:   NewItemRegistry(queueCap * 2),
		downloader: NewDownloader(outputDir),
	}
	m.runCtx, m.runCancel = context.WithCancel(context.Background())

	// Set up downloader callbacks
	m.downloader.SetProgressCallback(m.updateProgress)
	m.downloader.SetFilenameCallback(m.setFilename)

	// Start workers
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
	return m
}

// SetStore configures the store for persisting progress and state updates
func (m *Manager) SetStore(store Store) {
	m.store = store
}

// SetRegistry allows replacing the registry (useful for testing)
func (m *Manager) SetRegistry(registry *ItemRegistry) {
	m.registry = registry
}

// SetDownloader allows replacing the downloader (useful for testing)
func (m *Manager) SetDownloader(downloader *Downloader) {
	m.downloader = downloader
	// Re-setup callbacks
	m.downloader.SetProgressCallback(m.updateProgress)
	m.downloader.SetFilenameCallback(m.setFilename)
}

// StopAccepting stops queueing new jobs; Enqueue will return an error afterwards.
func (m *Manager) StopAccepting() {
	m.closing.Store(true)
}

// Shutdown cancels workers after current job; safe to call multiple times.
func (m *Manager) Shutdown() {
	// Mark manager as closing to stop new enqueues
	m.closing.Store(true)
	if m.runCancel != nil {
		m.runCancel()
	}
	// Close the jobs channel exactly once
	m.shutdownOnce.Do(func() {
		close(m.jobs)
	})
	// Wait for workers to finish current job
	m.wg.Wait()
	// Wait for background goroutines (progress and status updates) to complete
	m.bgWg.Wait()
}

// Enqueue adds a new URL to the queue and returns the assigned ID.
func (m *Manager) Enqueue(url string) (string, error) {
	if m.closing.Load() {
		return "", ErrShuttingDown
	}

	id := genID()

	// Create the item in the registry
	_, err := m.registry.Create(id, url)
	if err != nil {
		return "", fmt.Errorf("failed to create item: %w", err)
	}

	select {
	case m.jobs <- job{id: id, url: url}:
		return id, nil
	default:
		// queue full, remove the entry we just added
		m.registry.Delete(id)
		return "", ErrQueueFull
	}
}

// AttachDB binds a database row ID to the in-memory item for persistence updates.
func (m *Manager) AttachDB(id string, dbID int64) {
	if err := m.registry.Attach(id, dbID); err != nil {
		// Log but don't fail - this is a best-effort operation
		slog.Debug("failed to attach DB ID to item", "id", id, "db_id", dbID, "error", err)
	}
}

// SetMeta updates the in-memory item with extracted metadata for UI.
func (m *Manager) SetMeta(id string, title string, duration int64, thumb string) {
	if err := m.registry.SetMeta(id, title, duration, thumb); err != nil {
		// Log but don't fail - this is a best-effort operation
		slog.Debug("failed to set metadata for item", "id", id, "error", err)
	}
}

// Snapshot returns a copy of the current download items. If id is non-empty, returns at most that item.
func (m *Manager) Snapshot(id string) []*Item {
	return m.registry.Snapshot(id)
}

func (m *Manager) worker(idx int) {
	defer m.wg.Done()
	for j := range m.jobs {
		m.updateState(j.id, StateDownloading, "")

		ctx := m.runCtx
		if ctx == nil {
			ctx = context.Background()
		}
		downloadFn := m.workerDownload
		if downloadFn == nil {
			downloadFn = m.downloader.Download
		}

		if err := downloadFn(ctx, j.id, j.url); err != nil {
			m.updateFailure(j.id, err)
		} else {
			m.updateProgress(j.id, 100)
			m.updateState(j.id, StateCompleted, "")
		}
	}
}

// CheckYTDLP ensures yt-dlp is in PATH.
func CheckYTDLP() error {
	// Ensure yt-dlp exists
	p, err := exec.LookPath("yt-dlp")
	if err != nil {
		return err
	}
	// Ensure it supports --progress-template for JSON output.
	// This is available in modern versions of yt-dlp.
	out, err := exec.Command(p, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("yt-dlp not runnable: %w", err)
	}
	if !strings.Contains(string(out), "--progress-template") {
		return fmt.Errorf("yt_dlp_outdated: missing --progress-template support")
	}
	return nil
}

func (m *Manager) updateProgress(id string, p float64) {
	prev, new, err := m.registry.SetProgress(id, p)
	if err != nil {
		// Item might have been removed
		return
	}

	// Only process if progress actually changed
	if new == prev {
		return
	}

	// Log when integer percentage advances to reduce noise
	if int(new) != int(prev) {
		item := m.registry.Get(id)
		if item != nil {
			dbIDStr := ""
			if item.DBID > 0 {
				dbIDStr = fmt.Sprintf("%d", item.DBID)
			}
			logging.LogDownloadProgress(id, dbIDStr, new, item.URL)

			// Update store if configured
			if item.DBID > 0 && m.store != nil {
				dbid := item.DBID
				prog := new
				m.bgWg.Add(1)
				go func() {
					defer m.bgWg.Done()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := m.store.UpdateProgress(ctx, dbid, prog); err != nil {
						// Ignore expected errors during shutdown
						if !isExpectedShutdownError(err) {
							slog.Error("failed to update progress in store",
								"event", "store_update_error",
								"operation", "update_progress",
								"db_id", dbid,
								"error", err)
						}
					}
				}()
			}
		}
	}
}

func (m *Manager) updateState(id string, st State, errMsg string) {
	if err := m.registry.SetState(id, st, errMsg); err != nil {
		// Item might have been removed
		return
	}

	item := m.registry.Get(id)
	if item != nil {
		logging.LogDownloadStateChange(id, item.URL, string(st))

		if item.DBID > 0 && m.store != nil {
			dbid := item.DBID
			// Map State to status string
			var statusStr string
			switch st {
			case StateQueued:
				statusStr = "pending"
			case StateDownloading:
				statusStr = "downloading"
			case StateCompleted:
				statusStr = "completed"
			case StateFailed:
				statusStr = "error"
			default:
				statusStr = "pending"
			}
			m.bgWg.Add(1)
			go func() {
				defer m.bgWg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.store.UpdateStatus(ctx, dbid, statusStr, errMsg); err != nil {
					// Ignore expected errors during shutdown
					if !isExpectedShutdownError(err) {
						slog.Error("failed to update status in store",
							"event", "store_update_error",
							"operation", "update_status",
							"db_id", dbid,
							"status", statusStr,
							"error", err)
					}
				}
			}()
		}
	}
}

func (m *Manager) updateFailure(id string, err error) {
	msg := err.Error()
	// reduce noise from long command errors, respecting UTF-8 boundaries
	msg = truncateUTF8(msg, 512)
	m.updateState(id, StateFailed, msg)
}

// setFilename updates the filename for an item and calls the hook
func (m *Manager) setFilename(id, filename string) {
	if err := m.registry.SetFilename(id, filename); err != nil {
		// Item might have been removed
		return
	}

	item := m.registry.Get(id)
	if item != nil {
		dbIDStr := ""
		if item.DBID > 0 {
			dbIDStr = fmt.Sprintf("%d", item.DBID)
		}
		logging.LogDownloadComplete(id, dbIDStr, filename)

		if item.DBID > 0 && m.store != nil {
			dbid := item.DBID
			fname := filename
			m.bgWg.Add(1)
			go func() {
				defer m.bgWg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.store.UpdateFilename(ctx, dbid, fname); err != nil {
					// Ignore expected errors during shutdown
					if !isExpectedShutdownError(err) {
						slog.Error("failed to update filename in store",
							"event", "store_update_error",
							"operation", "update_filename",
							"db_id", dbid,
							"filename", fname,
							"error", err)
					}
				}
			}()
		}
	}
}

// ProcessPendingDownload processes a single pending download from the database.
func (m *Manager) ProcessPendingDownload(ctx context.Context, dbID int64, url string, store PendingDownloadStore) error {
	if ctx == nil {
		ctx = context.Background()
	}

	claimed, err := store.TryClaimPending(ctx, dbID)
	if err != nil {
		return fmt.Errorf("failed to claim pending download %d: %w", dbID, err)
	}
	if !claimed {
		slog.Debug("pending download already claimed",
			"event", "pending_claim_conflict",
			"db_id", dbID,
			"url", logging.RedactURL(url))
		return nil
	}

	// Fetch media info
	metaCtx, metaCancel := context.WithTimeout(ctx, pendingMetadataTimeout)
	defer metaCancel()
	mediaInfo, err := fetchMediaInfo(metaCtx, url)
	if err != nil {
		logging.LogMetadataFetch(url, dbID, err)
		// Update database with error
		if updateErr := store.UpdateStatus(ctx, dbID, "failed", fmt.Sprintf("metadata_fetch_failed: %v", err)); updateErr != nil {
			slog.Error("failed to update error status in ProcessPendingDownload",
				"event", "store_update_error",
				"operation", "update_status_on_metadata_failure",
				"db_id", dbID,
				"error", updateErr)
		}
		return fmt.Errorf("metadata fetch failed: %w", err)
	}

	// Update database with metadata
	if err := store.UpdateMeta(ctx, dbID, mediaInfo.Title, mediaInfo.DurationSec, mediaInfo.ThumbnailURL); err != nil {
		slog.Error("failed to update metadata in ProcessPendingDownload",
			"event", "store_update_error",
			"operation", "update_metadata",
			"db_id", dbID,
			"error", err)
	} else {
		logging.LogMetadataFetch(url, dbID, nil)
	}

	// Enqueue the download with the manager
	id, err := m.Enqueue(url)
	if err != nil {
		slog.Error("failed to enqueue download in ProcessPendingDownload",
			"event", "enqueue_error",
			"db_id", dbID,
			"url", logging.RedactURL(url),
			"error", err)
		// Update database with error
		if updateErr := store.UpdateStatus(ctx, dbID, "failed", fmt.Sprintf("enqueue_failed: %v", err)); updateErr != nil {
			slog.Error("failed to update error status in ProcessPendingDownload",
				"event", "store_update_error",
				"operation", "update_status_on_enqueue_failure",
				"db_id", dbID,
				"error", updateErr)
		}
		return fmt.Errorf("enqueue failed: %w", err)
	}

	// Attach the database ID to the manager item for progress updates
	m.AttachDB(id, dbID)
	m.SetMeta(id, mediaInfo.Title, mediaInfo.DurationSec, mediaInfo.ThumbnailURL)

	slog.Info("ProcessPendingDownload: download enqueued",
		"event", "download_enqueued",
		"url", logging.RedactURL(url),
		"db_id", dbID,
		"manager_id", id)
	return nil
}

func genID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// fallback
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// truncateUTF8 truncates a string to at most n bytes while preserving UTF-8 validity
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}

	// Start from position n and work backwards to find a valid UTF-8 boundary
	// A valid UTF-8 boundary is either at position 0 or where the byte is not a continuation byte
	for i := n; i >= 0; i-- {
		// UTF-8 continuation bytes have the form 10xxxxxx (0x80-0xBF)
		// A valid boundary is where we're not in the middle of a multi-byte sequence
		if i == 0 || (s[i]&0xC0) != 0x80 {
			// Verify this is actually a valid UTF-8 string
			if utf8.ValidString(s[:i]) {
				return s[:i]
			}
		}
	}
	// Fallback: empty string if no valid UTF-8 found
	return ""
}

// isExpectedShutdownError checks if an error is expected during shutdown
func isExpectedShutdownError(err error) bool {
	if err == nil {
		return false
	}
	// These are system-level errors from database/sql and context packages
	// that don't expose typed errors, so string checking is necessary here
	errStr := err.Error()
	return errStr == "sql: database is closed" ||
		errStr == "context deadline exceeded" ||
		errStr == "context canceled"
}
