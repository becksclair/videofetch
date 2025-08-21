package download

import (
	"context"
	"fmt"
	"log"
	"time"
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
				log.Printf("dbworker: error processing pending URLs: %v", err)
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
			log.Printf("dbworker: invalid download type: %T", download)
			continue
		}

		downloadID, ok := downloadMap["id"].(int64)
		if !ok {
			log.Printf("dbworker: invalid download ID type")
			continue
		}

		// Update status to downloading to prevent duplicate processing
		if err := dw.store.UpdateStatus(dw.ctx, downloadID, "downloading", ""); err != nil {
			log.Printf("dbworker: failed to update status for download %d: %v", downloadID, err)
			continue
		}

		// Fetch metadata asynchronously
		go dw.processDownload(downloadMap)
	}

	return nil
}

func (dw *DBWorker) processDownload(download map[string]interface{}) {
	downloadID := download["id"].(int64)
	downloadURL := download["url"].(string)

	// Fetch media info
	mediaInfo, err := FetchMediaInfo(downloadURL)
	if err != nil {
		log.Printf("dbworker: failed to fetch metadata for %s: %v", downloadURL, err)
		// Update database with error
		if updateErr := dw.store.UpdateStatus(dw.ctx, downloadID, "failed", fmt.Sprintf("metadata_fetch_failed: %v", err)); updateErr != nil {
			log.Printf("dbworker: failed to update error status for download %d: %v", downloadID, updateErr)
		}
		return
	}

	// Update database with metadata
	if err := dw.store.UpdateMeta(dw.ctx, downloadID, mediaInfo.Title, mediaInfo.DurationSec, mediaInfo.ThumbnailURL); err != nil {
		log.Printf("dbworker: failed to update metadata for download %d: %v", downloadID, err)
	}

	// Enqueue the download with the manager
	id, err := dw.manager.Enqueue(downloadURL)
	if err != nil {
		log.Printf("dbworker: failed to enqueue %s: %v", downloadURL, err)
		// Update database with error
		if updateErr := dw.store.UpdateStatus(dw.ctx, downloadID, "failed", fmt.Sprintf("enqueue_failed: %v", err)); updateErr != nil {
			log.Printf("dbworker: failed to update error status for download %d: %v", downloadID, updateErr)
		}
		return
	}

	// Attach the database ID to the manager item for progress updates
	dw.manager.AttachDB(id, downloadID)
	dw.manager.SetMeta(id, mediaInfo.Title, mediaInfo.DurationSec, mediaInfo.ThumbnailURL)

	log.Printf("dbworker: enqueued %s (db_id=%d, manager_id=%s)", downloadURL, downloadID, id)
}

// RetryIncompleteDownloads retries all downloads that are not completed at startup
func (dw *DBWorker) RetryIncompleteDownloads() error {
	log.Printf("dbworker: checking for incomplete downloads to retry...")

	incomplete, err := dw.store.GetIncompleteDownloads(dw.ctx, 100) // check up to 100 incomplete downloads
	if err != nil {
		return fmt.Errorf("failed to get incomplete downloads: %w", err)
	}

	if len(incomplete) == 0 {
		log.Printf("dbworker: no incomplete downloads found")
		return nil
	}

	log.Printf("dbworker: found %d incomplete downloads, retrying...", len(incomplete))

	for _, download := range incomplete {
		select {
		case <-dw.ctx.Done():
			return nil
		default:
		}

		// Reset status to pending so the regular worker can pick it up
		if err := dw.store.UpdateStatus(dw.ctx, download.GetID(), "pending", ""); err != nil {
			log.Printf("dbworker: failed to reset status for download %d: %v", download.GetID(), err)
			continue
		}

		log.Printf("dbworker: reset download %d (url=%s, status=%s, progress=%.1f) to pending for retry",
			download.GetID(), download.GetURL(), download.GetStatus(), download.GetProgress())
	}

	log.Printf("dbworker: startup retry complete, reset %d downloads to pending", len(incomplete))
	return nil
}
