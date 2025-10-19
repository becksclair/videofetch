package download

import (
	"testing"
	"time"
)

// TestManagerWithComponents tests that the Manager works correctly with
// the new ItemRegistry and Downloader components after refactoring
func TestManagerWithComponents(t *testing.T) {
	// Create a manager with custom components
	m := NewManager(t.TempDir(), 2, 10)
	defer m.Shutdown()

	// Test that we can enqueue items
	id1, err := m.Enqueue("http://example.com/video1")
	if err != nil {
		t.Fatalf("failed to enqueue item: %v", err)
	}
	if id1 == "" {
		t.Error("expected non-empty ID")
	}

	_, err = m.Enqueue("http://example.com/video2")
	if err != nil {
		t.Fatalf("failed to enqueue second item: %v", err)
	}

	// Test snapshot functionality
	items := m.Snapshot("")
	if len(items) < 2 {
		t.Errorf("expected at least 2 items in snapshot, got %d", len(items))
	}

	// Test getting specific item
	items = m.Snapshot(id1)
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
	if items[0].ID != id1 {
		t.Errorf("expected ID %s, got %s", id1, items[0].ID)
	}

	// Test AttachDB
	m.AttachDB(id1, 12345)
	item := m.Snapshot(id1)[0]
	if item.DBID != 12345 {
		t.Errorf("expected DBID 12345, got %d", item.DBID)
	}

	// Test SetMeta
	m.SetMeta(id1, "Test Video", 120, "http://thumb.jpg")
	item = m.Snapshot(id1)[0]
	if item.Title != "Test Video" {
		t.Errorf("expected title 'Test Video', got %s", item.Title)
	}
	if item.Duration != 120 {
		t.Errorf("expected duration 120, got %d", item.Duration)
	}

	// Test StopAccepting
	m.StopAccepting()
	_, err = m.Enqueue("http://example.com/video3")
	if err != ErrShuttingDown {
		t.Errorf("expected ErrShuttingDown, got %v", err)
	}
}

// TestManagerQueueFull tests that the queue full error is properly returned
func TestManagerQueueFull(t *testing.T) {
	// Create a manager with 0 workers and a very small queue
	// 0 workers means items won't be processed, ensuring queue fills up
	m := &Manager{
		outDir:     t.TempDir(),
		jobs:       make(chan job, 2), // Queue capacity of 2
		registry:   NewItemRegistry(10),
		downloader: NewDownloader(t.TempDir()),
	}
	defer m.Shutdown()

	// Fill the queue
	ids := make([]string, 0, 3)
	for i := 0; i < 2; i++ {
		id, err := m.Enqueue("http://example.com/video")
		if err != nil {
			t.Fatalf("failed to enqueue item %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Try to add one more - should fail with queue full
	_, err := m.Enqueue("http://example.com/overflow")
	if err != ErrQueueFull {
		t.Errorf("expected ErrQueueFull, got %v", err)
	}

	// Verify the items that were enqueued are still there
	items := m.Snapshot("")
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

// TestManagerCustomComponents tests using custom registry and downloader
func TestManagerCustomComponents(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 10)
	defer m.Shutdown()

	// Create custom registry
	customRegistry := NewItemRegistry(20)
	m.SetRegistry(customRegistry)

	// Create custom downloader with tracking callback
	customDownloader := NewDownloader(t.TempDir())
	customDownloader.SetProgressCallback(func(id string, progress float64) {
		// Delegate to manager's updateProgress
		m.updateProgress(id, progress)
	})
	customDownloader.SetFilenameCallback(func(id string, filename string) {
		// Delegate to manager's setFilename
		m.setFilename(id, filename)
	})
	m.SetDownloader(customDownloader)

	// Enqueue should work with custom components
	id, err := m.Enqueue("http://example.com/video")
	if err != nil {
		t.Fatalf("failed to enqueue with custom components: %v", err)
	}

	// Verify item exists in custom registry
	if customRegistry.Get(id) == nil {
		t.Error("item not found in custom registry")
	}

	// Verify snapshot still works
	items := m.Snapshot("")
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

// TestManagerConcurrentOperations tests thread safety of the refactored manager
func TestManagerConcurrentOperations(t *testing.T) {
	m := NewManager(t.TempDir(), 4, 100)
	defer m.Shutdown()

	// Run concurrent operations
	done := make(chan bool)

	// Enqueuers
	for i := 0; i < 3; i++ {
		go func(n int) {
			for j := 0; j < 10; j++ {
				m.Enqueue("http://example.com/video")
				time.Sleep(time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Snapshot readers
	for i := 0; i < 2; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				items := m.Snapshot("")
				_ = len(items)
				time.Sleep(time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Manager should still be functional
	items := m.Snapshot("")
	if len(items) == 0 {
		t.Error("expected some items after concurrent operations")
	}
}
