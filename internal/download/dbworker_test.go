package download

import (
	"context"
	"testing"
)

// Mock store for testing
type mockStore struct {
	incompleteDownloads []incompleteDownload
	updateStatusCalls   []updateStatusCall
}

type incompleteDownload struct {
	id           int64
	url          string
	title        string
	duration     int64
	thumbnailURL string
	status       string
	progress     float64
}

func (d *incompleteDownload) GetID() int64           { return d.id }
func (d *incompleteDownload) GetURL() string         { return d.url }
func (d *incompleteDownload) GetTitle() string       { return d.title }
func (d *incompleteDownload) GetDuration() int64     { return d.duration }
func (d *incompleteDownload) GetThumbnailURL() string { return d.thumbnailURL }
func (d *incompleteDownload) GetStatus() string      { return d.status }
func (d *incompleteDownload) GetProgress() float64   { return d.progress }

type updateStatusCall struct {
	id     int64
	status string
	errMsg string
}

func (m *mockStore) GetPendingDownloadsForWorker(ctx context.Context, limit int) ([]interface{}, error) {
	return []interface{}{}, nil
}

func (m *mockStore) GetIncompleteDownloads(ctx context.Context, limit int) ([]interface {
	GetID() int64
	GetURL() string
	GetTitle() string
	GetDuration() int64
	GetThumbnailURL() string
	GetStatus() string
	GetProgress() float64
}, error) {
	result := make([]interface {
		GetID() int64
		GetURL() string
		GetTitle() string
		GetDuration() int64
		GetThumbnailURL() string
		GetStatus() string
		GetProgress() float64
	}, len(m.incompleteDownloads))
	for i, d := range m.incompleteDownloads {
		result[i] = &d
	}
	return result, nil
}

func (m *mockStore) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
	m.updateStatusCalls = append(m.updateStatusCalls, updateStatusCall{id, status, errMsg})
	return nil
}

func (m *mockStore) UpdateMeta(ctx context.Context, id int64, title string, duration int64, thumbnail string) error {
	return nil
}

func TestRetryIncompleteDownloads_NoIncompleteDownloads(t *testing.T) {
	store := &mockStore{}
	mgr := NewManager("/tmp", 1, 10)
	
	dbWorker := NewDBWorker(store, mgr)
	
	err := dbWorker.RetryIncompleteDownloads()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	
	if len(store.updateStatusCalls) != 0 {
		t.Errorf("expected no status updates, got %d", len(store.updateStatusCalls))
	}
	
	// Clean up
	dbWorker.Stop()
	mgr.Shutdown()
}

func TestRetryIncompleteDownloads_WithIncompleteDownloads(t *testing.T) {
	store := &mockStore{
		incompleteDownloads: []incompleteDownload{
			{id: 1, url: "https://example.com/video1", title: "Video 1", status: "downloading", progress: 50.0},
			{id: 2, url: "https://example.com/video2", title: "Video 2", status: "error", progress: 0.0},
			{id: 3, url: "https://example.com/video3", title: "Video 3", status: "completed", progress: 95.0}, // completed but not 100% - edge case
		},
	}
	
	mgr := NewManager("/tmp", 1, 10)
	dbWorker := NewDBWorker(store, mgr)
	
	err := dbWorker.RetryIncompleteDownloads()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	
	// All incomplete downloads should be reset to pending
	if len(store.updateStatusCalls) != 3 {
		t.Errorf("expected 3 status updates, got %d", len(store.updateStatusCalls))
	}
	
	for i, call := range store.updateStatusCalls {
		expectedID := int64(i + 1)
		if call.id != expectedID {
			t.Errorf("update %d: expected ID %d, got %d", i, expectedID, call.id)
		}
		if call.status != "pending" {
			t.Errorf("update %d: expected status 'pending', got %s", i, call.status)
		}
		if call.errMsg != "" {
			t.Errorf("update %d: expected empty error message, got %s", i, call.errMsg)
		}
	}
	
	// Clean up
	dbWorker.Stop()
	mgr.Shutdown()
}

func TestRetryIncompleteDownloads_ContextCancellation(t *testing.T) {
	store := &mockStore{
		incompleteDownloads: []incompleteDownload{
			{id: 1, url: "https://example.com/video1", title: "Video 1", status: "downloading", progress: 50.0},
			{id: 2, url: "https://example.com/video2", title: "Video 2", status: "error", progress: 0.0},
		},
	}
	
	mgr := NewManager("/tmp", 1, 10)
	dbWorker := NewDBWorker(store, mgr)
	
	// Cancel the context immediately
	dbWorker.cancel()
	
	err := dbWorker.RetryIncompleteDownloads()
	if err != nil {
		t.Fatalf("expected no error on context cancellation, got: %v", err)
	}
	
	// Should have stopped early, so no updates
	if len(store.updateStatusCalls) != 0 {
		t.Errorf("expected no status updates due to context cancellation, got %d", len(store.updateStatusCalls))
	}
	
	// Clean up
	dbWorker.Stop()
	mgr.Shutdown()
}