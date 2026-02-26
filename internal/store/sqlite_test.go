package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close()

	// Verify database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestCreateDownload(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "https://example.com/thumb.jpg", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	if id <= 0 {
		t.Error("Expected positive ID, got", id)
	}
}

func TestCreateDownload_EmptyURL(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	_, err := store.CreateDownload(ctx, "", "Test Video", 300, "", "pending", 0.0)
	if err == nil {
		t.Error("Expected error for empty URL")
	}
	if !errors.Is(err, ErrEmptyURL) {
		t.Errorf("Expected ErrEmptyURL error, got: %v", err)
	}
}

func TestUpdateProgress(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	err = store.UpdateProgress(ctx, id, 50.0)
	if err != nil {
		t.Fatalf("UpdateProgress() failed: %v", err)
	}

	// Verify progress was updated
	downloads, err := store.ListDownloads(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	if len(downloads) != 1 {
		t.Fatalf("Expected 1 download, got %d", len(downloads))
	}

	if downloads[0].Progress != 50.0 {
		t.Errorf("Expected progress 50.0, got %f", downloads[0].Progress)
	}
}

func TestUpdateStatus(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	err = store.UpdateStatus(ctx, id, "downloading", "")
	if err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	// Verify status was updated
	downloads, err := store.ListDownloads(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	if len(downloads) != 1 {
		t.Fatalf("Expected 1 download, got %d", len(downloads))
	}

	if downloads[0].Status != "downloading" {
		t.Errorf("Expected status 'downloading', got %s", downloads[0].Status)
	}
	if downloads[0].ErrorMessage != "" {
		t.Errorf("expected empty error_message after non-error status, got %q", downloads[0].ErrorMessage)
	}
}

func TestUpdateStatus_PersistErrorAndClear(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	err = store.UpdateStatus(ctx, id, "error", "metadata_fetch_failed: token=abc123")
	if err != nil {
		t.Fatalf("UpdateStatus(error) failed: %v", err)
	}

	d, found, err := store.GetDownloadByID(ctx, id)
	if err != nil {
		t.Fatalf("GetDownloadByID() failed: %v", err)
	}
	if !found {
		t.Fatalf("expected download to exist")
	}
	if d.Status != "error" {
		t.Fatalf("expected status=error, got %s", d.Status)
	}
	if d.ErrorMessage == "" {
		t.Fatalf("expected persisted error message")
	}

	err = store.UpdateStatus(ctx, id, "completed", "")
	if err != nil {
		t.Fatalf("UpdateStatus(completed) failed: %v", err)
	}

	d, found, err = store.GetDownloadByID(ctx, id)
	if err != nil {
		t.Fatalf("GetDownloadByID() after clear failed: %v", err)
	}
	if !found {
		t.Fatalf("expected download to exist after status update")
	}
	if d.Status != "completed" {
		t.Fatalf("expected status=completed, got %s", d.Status)
	}
	if d.ErrorMessage != "" {
		t.Fatalf("expected error_message to clear on non-error status, got %q", d.ErrorMessage)
	}
}

func TestGetDownloadByID(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	if err := store.UpdateFilename(ctx, id, "video.mp4"); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}
	if err := store.UpdateStatus(ctx, id, "error", "download command failed"); err != nil {
		t.Fatalf("UpdateStatus(error) failed: %v", err)
	}

	got, found, err := store.GetDownloadByID(ctx, id)
	if err != nil {
		t.Fatalf("GetDownloadByID(found) failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if got.ID != id {
		t.Fatalf("expected ID=%d got %d", id, got.ID)
	}
	if got.Filename != "video.mp4" {
		t.Fatalf("expected filename video.mp4 got %q", got.Filename)
	}
	if got.ErrorMessage != "download command failed" {
		t.Fatalf("expected error_message to round-trip, got %q", got.ErrorMessage)
	}

	_, found, err = store.GetDownloadByID(ctx, id+9999)
	if err != nil {
		t.Fatalf("GetDownloadByID(not found) failed: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing row")
	}
}

func TestOpen_MigratesLegacySchemaWithErrorMessage(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "legacy.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() failed: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS downloads (
	id INTEGER PRIMARY KEY,
	url TEXT NOT NULL,
	title TEXT,
	duration INTEGER,
	thumbnail_url TEXT,
	status TEXT,
	progress REAL,
	filename TEXT,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		t.Fatalf("creating legacy schema failed: %v", err)
	}
	_ = db.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(legacy DB) failed: %v", err)
	}
	defer store.Close()

	hasErrorColumn, err := hasColumn(store.db, "downloads", "error_message")
	if err != nil {
		t.Fatalf("hasColumn() failed: %v", err)
	}
	if !hasErrorColumn {
		t.Fatalf("expected migration to add error_message column")
	}

	hasArtifactsColumn, err := hasColumn(store.db, "downloads", "artifact_paths")
	if err != nil {
		t.Fatalf("hasColumn() failed: %v", err)
	}
	if !hasArtifactsColumn {
		t.Fatalf("expected migration to add artifact_paths column")
	}
}

func TestUpdateArtifacts_RoundTrip(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	paths := []string{
		"/tmp/out/a.part",
		"/tmp/out/b.part",
		"/tmp/out/a.part",
	}
	if err := store.UpdateArtifacts(ctx, id, paths); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}

	row, found, err := store.GetDownloadByID(ctx, id)
	if err != nil {
		t.Fatalf("GetDownloadByID() failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(row.ArtifactPaths) != 2 {
		t.Fatalf("expected deduped artifact paths length 2, got %d (%v)", len(row.ArtifactPaths), row.ArtifactPaths)
	}
	if row.ArtifactPaths[0] != "/tmp/out/a.part" || row.ArtifactPaths[1] != "/tmp/out/b.part" {
		t.Fatalf("unexpected artifact paths: %v", row.ArtifactPaths)
	}
}

func TestTryClaimPending(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	claimed, err := store.TryClaimPending(ctx, id)
	if err != nil {
		t.Fatalf("TryClaimPending() failed: %v", err)
	}
	if !claimed {
		t.Fatalf("expected initial claim to succeed")
	}

	claimedAgain, err := store.TryClaimPending(ctx, id)
	if err != nil {
		t.Fatalf("TryClaimPending() second call failed: %v", err)
	}
	if claimedAgain {
		t.Fatalf("expected second claim to fail for non-pending row")
	}

	downloads, err := store.ListDownloads(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}
	if len(downloads) != 1 {
		t.Fatalf("expected 1 row, got %d", len(downloads))
	}
	if downloads[0].Status != "downloading" {
		t.Fatalf("expected status downloading after claim, got %s", downloads[0].Status)
	}
}

func TestTryCancel(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	pendingID, err := store.CreateDownload(ctx, "https://example.com/pending-cancel", "Pending", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload(pending) failed: %v", err)
	}
	completedID, err := store.CreateDownload(ctx, "https://example.com/completed-cancel", "Completed", 0, "", "completed", 100)
	if err != nil {
		t.Fatalf("CreateDownload(completed) failed: %v", err)
	}

	canceled, err := store.TryCancel(ctx, pendingID)
	if err != nil {
		t.Fatalf("TryCancel(pending) failed: %v", err)
	}
	if !canceled {
		t.Fatalf("expected pending download to be canceled")
	}

	pendingRow, found, err := store.GetDownloadByID(ctx, pendingID)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID(pending) failed: found=%v err=%v", found, err)
	}
	if pendingRow.Status != "canceled" {
		t.Fatalf("expected pending row to be canceled, got %q", pendingRow.Status)
	}

	canceled, err = store.TryCancel(ctx, completedID)
	if err != nil {
		t.Fatalf("TryCancel(completed) failed: %v", err)
	}
	if canceled {
		t.Fatalf("did not expect completed download to be canceled")
	}

	completedRow, found, err := store.GetDownloadByID(ctx, completedID)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID(completed) failed: found=%v err=%v", found, err)
	}
	if completedRow.Status != "completed" {
		t.Fatalf("expected completed row to remain completed, got %q", completedRow.Status)
	}
}

func TestTryCancelNotDownloading(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	pendingID, err := store.CreateDownload(ctx, "https://example.com/pending-cancel-safe", "Pending", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload(pending) failed: %v", err)
	}
	downloadingID, err := store.CreateDownload(ctx, "https://example.com/downloading-cancel-safe", "Downloading", 0, "", "downloading", 42)
	if err != nil {
		t.Fatalf("CreateDownload(downloading) failed: %v", err)
	}

	canceled, err := store.TryCancelNotDownloading(ctx, pendingID)
	if err != nil {
		t.Fatalf("TryCancelNotDownloading(pending) failed: %v", err)
	}
	if !canceled {
		t.Fatalf("expected pending row to be canceled")
	}
	canceled, err = store.TryCancelNotDownloading(ctx, downloadingID)
	if err != nil {
		t.Fatalf("TryCancelNotDownloading(downloading) failed: %v", err)
	}
	if canceled {
		t.Fatalf("did not expect downloading row to be canceled")
	}
}

func TestTryPause(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	pendingID, err := store.CreateDownload(ctx, "https://example.com/pending-pause", "Pending", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload(pending) failed: %v", err)
	}
	downloadingID, err := store.CreateDownload(ctx, "https://example.com/downloading-pause", "Downloading", 0, "", "downloading", 42)
	if err != nil {
		t.Fatalf("CreateDownload(downloading) failed: %v", err)
	}

	paused, err := store.TryPause(ctx, pendingID)
	if err != nil {
		t.Fatalf("TryPause(pending) failed: %v", err)
	}
	if !paused {
		t.Fatalf("expected pending row to transition to paused")
	}
	paused, err = store.TryPause(ctx, downloadingID)
	if err != nil {
		t.Fatalf("TryPause(downloading) failed: %v", err)
	}
	if paused {
		t.Fatalf("did not expect downloading row to transition to paused")
	}
}

func TestUpdateMeta(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Original Title", 0, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	err = store.UpdateMeta(ctx, id, "Updated Title", 600, "https://example.com/new-thumb.jpg")
	if err != nil {
		t.Fatalf("UpdateMeta() failed: %v", err)
	}

	// Verify metadata was updated
	downloads, err := store.ListDownloads(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	if len(downloads) != 1 {
		t.Fatalf("Expected 1 download, got %d", len(downloads))
	}

	download := downloads[0]
	if download.Title != "Updated Title" {
		t.Errorf("Expected title 'Updated Title', got %s", download.Title)
	}
	if download.Duration != 600 {
		t.Errorf("Expected duration 600, got %d", download.Duration)
	}
	if download.ThumbnailURL != "https://example.com/new-thumb.jpg" {
		t.Errorf("Expected thumbnail URL 'https://example.com/new-thumb.jpg', got %s", download.ThumbnailURL)
	}
}

func TestUpdateMeta_EmptyValues(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	id, err := store.CreateDownload(ctx, "https://example.com/video", "Original Title", 300, "original-thumb.jpg", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	// UpdateMeta with empty values should not change anything
	err = store.UpdateMeta(ctx, id, "", 0, "")
	if err != nil {
		t.Fatalf("UpdateMeta() failed: %v", err)
	}

	// Verify original values are preserved
	downloads, err := store.ListDownloads(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	download := downloads[0]
	if download.Title != "Original Title" {
		t.Errorf("Title should be unchanged, got %s", download.Title)
	}
	if download.Duration != 300 {
		t.Errorf("Duration should be unchanged, got %d", download.Duration)
	}
	if download.ThumbnailURL != "original-thumb.jpg" {
		t.Errorf("Thumbnail should be unchanged, got %s", download.ThumbnailURL)
	}
}

func TestListDownloads_FilterByStatus(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create downloads with different statuses
	_, err := store.CreateDownload(ctx, "https://example.com/video1", "Video 1", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	_, err = store.CreateDownload(ctx, "https://example.com/video2", "Video 2", 400, "", "downloading", 50.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	_, err = store.CreateDownload(ctx, "https://example.com/video3", "Video 3", 500, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	// Test filtering by status
	downloads, err := store.ListDownloads(ctx, ListFilter{Status: "downloading"})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	if len(downloads) != 1 {
		t.Fatalf("Expected 1 download with status 'downloading', got %d", len(downloads))
	}

	if downloads[0].Status != "downloading" {
		t.Errorf("Expected status 'downloading', got %s", downloads[0].Status)
	}
}

func TestListDownloads_Sort(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create downloads with different titles
	_, err := store.CreateDownload(ctx, "https://example.com/video1", "B Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	_, err = store.CreateDownload(ctx, "https://example.com/video2", "A Video", 400, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	// Test sorting by title
	downloads, err := store.ListDownloads(ctx, ListFilter{Sort: "title", Order: "asc"})
	if err != nil {
		t.Fatalf("ListDownloads() failed: %v", err)
	}

	if len(downloads) != 2 {
		t.Fatalf("Expected 2 downloads, got %d", len(downloads))
	}

	if downloads[0].Title != "A Video" {
		t.Errorf("Expected first download to be 'A Video', got %s", downloads[0].Title)
	}
	if downloads[1].Title != "B Video" {
		t.Errorf("Expected second download to be 'B Video', got %s", downloads[1].Title)
	}
}

func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"queued", "pending"},
		{"downloading", "downloading"},
		{"completed", "completed"},
		{"paused", "paused"},
		{"canceled", "canceled"},
		{"failed", "error"},
		{"error", "error"},
		{"DOWNLOADING", "downloading"},
		{"  pending  ", "pending"},
		{"unknown", "pending"},
		{"", "pending"},
	}

	for _, test := range tests {
		result := normalizeStatus(test.input)
		if result != test.expected {
			t.Errorf("normalizeStatus(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestIsURLCompleted(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	testURL := "https://example.com/test-video"

	// Test with non-existent URL
	completed, err := store.IsURLCompleted(ctx, testURL)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed: %v", err)
	}
	if completed {
		t.Error("Expected false for non-existent URL")
	}

	// Create download with pending status
	id, err := store.CreateDownload(ctx, testURL, "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	// Should still return false for pending status
	completed, err = store.IsURLCompleted(ctx, testURL)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed: %v", err)
	}
	if completed {
		t.Error("Expected false for pending URL")
	}

	// Update to completed status
	err = store.UpdateStatus(ctx, id, "completed", "")
	if err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	// Should now return true
	completed, err = store.IsURLCompleted(ctx, testURL)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed: %v", err)
	}
	if !completed {
		t.Error("Expected true for completed URL")
	}

	// Test with error status - should return false
	err = store.UpdateStatus(ctx, id, "error", "test error")
	if err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	completed, err = store.IsURLCompleted(ctx, testURL)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed: %v", err)
	}
	if completed {
		t.Error("Expected false for error status URL")
	}
}

func TestIsURLCompleted_EmptyURL(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	_, err := store.IsURLCompleted(ctx, "")
	if err == nil {
		t.Error("Expected error for empty URL")
	}
	if !errors.Is(err, ErrEmptyURL) {
		t.Errorf("Expected ErrEmptyURL error, got: %v", err)
	}
}

func TestIsURLCompleted_MultipleURLs(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	url1 := "https://example.com/video1"
	url2 := "https://example.com/video2"

	// Create two downloads, one completed and one pending
	id1, err := store.CreateDownload(ctx, url1, "Video 1", 300, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	_, err = store.CreateDownload(ctx, url2, "Video 2", 400, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	// Test first URL - should be completed
	completed1, err := store.IsURLCompleted(ctx, url1)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed for url1: %v", err)
	}
	if !completed1 {
		t.Error("Expected true for completed URL1")
	}

	// Test second URL - should not be completed
	completed2, err := store.IsURLCompleted(ctx, url2)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed for url2: %v", err)
	}
	if completed2 {
		t.Error("Expected false for pending URL2")
	}

	// Create another completed entry with the same URL as url1 - should still work
	_, err = store.CreateDownload(ctx, url1, "Video 1 Again", 300, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	completed1Again, err := store.IsURLCompleted(ctx, url1)
	if err != nil {
		t.Fatalf("IsURLCompleted() failed for url1 again: %v", err)
	}
	if !completed1Again {
		t.Error("Expected true for URL1 with multiple completed entries")
	}

	// Clean up the extra entry
	_ = store.DeleteDownload(ctx, id1)
}

func TestGetLatestDownloadByURL(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	testURL := "https://example.com/same-url"
	_, err := store.CreateDownload(ctx, testURL, "First", 1, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("CreateDownload(first) failed: %v", err)
	}
	secondID, err := store.CreateDownload(ctx, testURL, "Second", 2, "", "paused", 12.5)
	if err != nil {
		t.Fatalf("CreateDownload(second) failed: %v", err)
	}

	got, found, err := store.GetLatestDownloadByURL(ctx, testURL)
	if err != nil {
		t.Fatalf("GetLatestDownloadByURL() failed: %v", err)
	}
	if !found {
		t.Fatalf("expected row to be found")
	}
	if got.ID != secondID {
		t.Fatalf("expected newest id %d, got %d", secondID, got.ID)
	}
	if got.Status != "paused" {
		t.Fatalf("expected status paused, got %q", got.Status)
	}
}

func TestGetIncompleteDownloads_ExcludesPausedAndCanceled(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	_, _ = store.CreateDownload(ctx, "https://example.com/pending", "Pending", 0, "", "pending", 0)
	_, _ = store.CreateDownload(ctx, "https://example.com/downloading", "Downloading", 0, "", "downloading", 10)
	_, _ = store.CreateDownload(ctx, "https://example.com/error", "Error", 0, "", "error", 30)
	_, _ = store.CreateDownload(ctx, "https://example.com/paused", "Paused", 0, "", "paused", 40)
	_, _ = store.CreateDownload(ctx, "https://example.com/canceled", "Canceled", 0, "", "canceled", 50)
	_, _ = store.CreateDownload(ctx, "https://example.com/completed", "Completed", 0, "", "completed", 100)

	rows, err := store.GetIncompleteDownloads(ctx, 100)
	if err != nil {
		t.Fatalf("GetIncompleteDownloads() failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 retryable rows, got %d", len(rows))
	}
	statuses := make(map[string]bool, len(rows))
	for _, row := range rows {
		statuses[row.GetStatus()] = true
	}
	if !statuses["pending"] || !statuses["downloading"] || !statuses["error"] {
		t.Fatalf("unexpected statuses in retryable set: %+v", statuses)
	}
}

func TestSubscribeChanges_ReceivesUpsertAndDelete(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()
	ctx := context.Background()
	changes, unsubscribe := store.SubscribeChanges(8)
	defer unsubscribe()

	id, err := store.CreateDownload(ctx, "https://example.com/watch?v=1", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	var createEvt ChangeEvent
	select {
	case createEvt = <-changes:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for create event")
	}
	if createEvt.Type != ChangeUpsert || createEvt.ID != id {
		t.Fatalf("unexpected create event: %+v", createEvt)
	}

	if err := store.DeleteDownload(ctx, id); err != nil {
		t.Fatalf("DeleteDownload() failed: %v", err)
	}

	var deleteEvt ChangeEvent
	select {
	case deleteEvt = <-changes:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for delete event")
	}
	if deleteEvt.Type != ChangeDelete || deleteEvt.ID != id {
		t.Fatalf("unexpected delete event: %+v", deleteEvt)
	}
}

func TestSubscribeChanges_UnsubscribeDuringEmitDoesNotPanic(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	_, unsubscribe := store.SubscribeChanges(1)

	const emitters = 8
	const emitsPerEmitter = 500
	var wg sync.WaitGroup
	wg.Add(emitters)
	for i := 0; i < emitters; i++ {
		go func(offset int64) {
			defer wg.Done()
			for j := int64(0); j < emitsPerEmitter; j++ {
				store.emitChange(ChangeEvent{Type: ChangeUpsert, ID: offset*emitsPerEmitter + j})
			}
		}(int64(i))
	}

	unsubscribe()
	wg.Wait()
}

func setupTestStore(t *testing.T) *Store {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test store: %v", err)
	}

	return store
}
