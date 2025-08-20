package store

import (
	"context"
	"os"
	"path/filepath"
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
	if err.Error() != "empty_url" {
		t.Errorf("Expected 'empty_url' error, got: %v", err)
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

func setupTestStore(t *testing.T) *Store {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test store: %v", err)
	}

	return store
}