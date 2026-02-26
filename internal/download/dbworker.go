package download

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"videofetch/internal/logging"
)

// DBStore interface for the store operations needed by DBWorker
type DBStore interface {
	GetPendingDownloadsForWorker(ctx context.Context, limit int) ([]interface{}, error)
	GetIncompleteDownloads(ctx context.Context, limit int) ([]interface {
		GetID() int64
		GetURL() string
		GetTitle() string
		GetDuration() int64
		GetThumbnailURL() string
		GetStatus() string
		GetProgress() float64
	}, error)
	TryClaimPending(ctx context.Context, id int64) (bool, error)
	UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error
	UpdateMeta(ctx context.Context, id int64, title string, duration int64, thumbnail string) error
}

// DBWorker processes pending downloads from the database
type DBWorker struct {
	store   DBStore
	manager *Manager
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewDBWorker creates a new database worker that processes pending URLs
func NewDBWorker(store DBStore, manager *Manager) *DBWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &DBWorker{
		store:   store,
		manager: manager,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

// Start begins processing pending URLs from the database in the background
func (dw *DBWorker) Start() {
	go dw.run()
}

// Stop stops the database worker
func (dw *DBWorker) Stop() {
	dw.cancel()
	<-dw.done
}

func (dw *DBWorker) run() {
	defer close(dw.done)

	ticker := time.NewTicker(2 * time.Second) // Poll database every 2 seconds
	defer ticker.Stop()

	for {
		select {
		case <-dw.ctx.Done():
			return
		case <-ticker.C:
			if err := dw.processPendingURLs(); err != nil {
				slog.Error("dbworker: error processing pending URLs",
					"event", "dbworker_error",
					"error", err)
			}
		}
	}
}

func (dw *DBWorker) processPendingURLs() error {
	// Get a batch of pending downloads
	pending, err := dw.store.GetPendingDownloadsForWorker(dw.ctx, 10)
	if err != nil {
		return fmt.Errorf("failed to get pending downloads: %w", err)
	}

	for _, download := range pending {
		select {
		case <-dw.ctx.Done():
			return nil
		default:
		}

		downloadMap, ok := download.(map[string]interface{})
		if !ok {
			slog.Error("dbworker: invalid download type",
				"event", "dbworker_type_error",
				"type", fmt.Sprintf("%T", download))
			continue
		}

		// Process metadata fetch and enqueue asynchronously
		go dw.processDownload(downloadMap)
	}

	return nil
}

func (dw *DBWorker) processDownload(download map[string]interface{}) {
	downloadID := download["id"].(int64)
	downloadURL := download["url"].(string)

	// Use the new helper function from Manager
	if err := dw.manager.ProcessPendingDownload(dw.ctx, downloadID, downloadURL, dw.store); err != nil {
		slog.Error("dbworker: ProcessPendingDownload failed",
			"event", "dbworker_process_error",
			"db_id", downloadID,
			"url", logging.RedactURL(downloadURL),
			"error", err)
		// Mark as failed to prevent infinite retries
		if updateErr := dw.store.UpdateStatus(dw.ctx, downloadID, "failed", fmt.Sprintf("ProcessPendingDownload failed: %v", err)); updateErr != nil {
			slog.Error("dbworker: failed to update status after ProcessPendingDownload error",
				"event", "dbworker_status_update_error",
				"db_id", downloadID,
				"error", updateErr)
		}
	}
}

// RetryIncompleteDownloads retries all downloads that are not completed at startup
func (dw *DBWorker) RetryIncompleteDownloads() error {
	slog.Info("dbworker: checking for incomplete downloads to retry...")

	incomplete, err := dw.store.GetIncompleteDownloads(dw.ctx, 100) // check up to 100 incomplete downloads
	if err != nil {
		return fmt.Errorf("failed to get incomplete downloads: %w", err)
	}

	if len(incomplete) == 0 {
		slog.Info("dbworker: no incomplete downloads found")
		return nil
	}

	slog.Info("dbworker: found incomplete downloads, retrying...",
		"event", "dbworker_retry_start",
		"count", len(incomplete))

	for _, download := range incomplete {
		select {
		case <-dw.ctx.Done():
			return nil
		default:
		}

		// Reset status to pending so the regular worker can pick it up
		if err := dw.store.UpdateStatus(dw.ctx, download.GetID(), "pending", ""); err != nil {
			slog.Error("dbworker: failed to reset status for download",
				"event", "dbworker_reset_error",
				"db_id", download.GetID(),
				"error", err)
			continue
		}

		slog.Info("dbworker: reset download to pending for retry",
			"event", "dbworker_reset",
			"db_id", download.GetID(),
			"url", logging.RedactURL(download.GetURL()),
			"status", download.GetStatus(),
			"progress", download.GetProgress())
	}

	slog.Info("dbworker: startup retry complete",
		"event", "dbworker_retry_complete",
		"count", len(incomplete))
	return nil
}
