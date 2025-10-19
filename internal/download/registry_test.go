package download

import (
	"sync"
	"testing"
)

func TestItemRegistry_Create(t *testing.T) {
	reg := NewItemRegistry(10)

	// Create a new item
	item, err := reg.Create("test-id", "http://example.com/video")
	if err != nil {
		t.Fatalf("failed to create item: %v", err)
	}
	if item.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got %s", item.ID)
	}
	if item.URL != "http://example.com/video" {
		t.Errorf("expected URL 'http://example.com/video', got %s", item.URL)
	}
	if item.State != StateQueued {
		t.Errorf("expected state %s, got %s", StateQueued, item.State)
	}

	// Try to create duplicate - should fail
	_, err = reg.Create("test-id", "http://other.com")
	if err == nil {
		t.Error("expected error when creating duplicate item")
	}
}

func TestItemRegistry_Get(t *testing.T) {
	reg := NewItemRegistry(10)

	// Get non-existent item
	item := reg.Get("missing")
	if item != nil {
		t.Error("expected nil for non-existent item")
	}

	// Create and get item
	reg.Create("test-id", "http://example.com")
	item = reg.Get("test-id")
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got %s", item.ID)
	}
}

func TestItemRegistry_Update(t *testing.T) {
	reg := NewItemRegistry(10)

	// Update non-existent item
	err := reg.Update("missing", func(it *Item) {
		it.Progress = 50
	})
	if err == nil {
		t.Error("expected error when updating non-existent item")
	}

	// Create and update item
	reg.Create("test-id", "http://example.com")
	err = reg.Update("test-id", func(it *Item) {
		it.Progress = 75
		it.State = StateDownloading
	})
	if err != nil {
		t.Fatalf("failed to update item: %v", err)
	}

	item := reg.Get("test-id")
	if item.Progress != 75 {
		t.Errorf("expected progress 75, got %f", item.Progress)
	}
	if item.State != StateDownloading {
		t.Errorf("expected state %s, got %s", StateDownloading, item.State)
	}
}

func TestItemRegistry_SetProgress(t *testing.T) {
	reg := NewItemRegistry(10)
	reg.Create("test-id", "http://example.com")

	// Set progress
	prev, new, err := reg.SetProgress("test-id", 50)
	if err != nil {
		t.Fatalf("failed to set progress: %v", err)
	}
	if prev != 0 {
		t.Errorf("expected prev 0, got %f", prev)
	}
	if new != 50 {
		t.Errorf("expected new 50, got %f", new)
	}

	// Try to decrease progress - should not change
	prev, new, err = reg.SetProgress("test-id", 30)
	if err != nil {
		t.Fatalf("failed to set progress: %v", err)
	}
	if prev != 50 {
		t.Errorf("expected prev 50, got %f", prev)
	}
	if new != 50 {
		t.Errorf("expected new 50 (unchanged), got %f", new)
	}

	// Increase progress
	prev, new, err = reg.SetProgress("test-id", 80)
	if err != nil {
		t.Fatalf("failed to set progress: %v", err)
	}
	if prev != 50 {
		t.Errorf("expected prev 50, got %f", prev)
	}
	if new != 80 {
		t.Errorf("expected new 80, got %f", new)
	}
}

func TestItemRegistry_Snapshot(t *testing.T) {
	reg := NewItemRegistry(10)

	// Empty registry
	items := reg.Snapshot("")
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}

	// Add items
	reg.Create("id1", "http://example.com/1")
	reg.Create("id2", "http://example.com/2")
	reg.Create("id3", "http://example.com/3")

	// Snapshot all
	items = reg.Snapshot("")
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}

	// Snapshot specific item
	items = reg.Snapshot("id2")
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
	if items[0].ID != "id2" {
		t.Errorf("expected ID 'id2', got %s", items[0].ID)
	}

	// Snapshot non-existent item
	items = reg.Snapshot("missing")
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestItemRegistry_Attach(t *testing.T) {
	reg := NewItemRegistry(10)
	reg.Create("test-id", "http://example.com")

	// Attach DB ID
	err := reg.Attach("test-id", 12345)
	if err != nil {
		t.Fatalf("failed to attach DB ID: %v", err)
	}

	item := reg.Get("test-id")
	if item.DBID != 12345 {
		t.Errorf("expected DBID 12345, got %d", item.DBID)
	}
}

func TestItemRegistry_SetMeta(t *testing.T) {
	reg := NewItemRegistry(10)
	reg.Create("test-id", "http://example.com")

	// Set metadata
	err := reg.SetMeta("test-id", "Video Title", 180, "http://thumb.jpg")
	if err != nil {
		t.Fatalf("failed to set metadata: %v", err)
	}

	item := reg.Get("test-id")
	if item.Title != "Video Title" {
		t.Errorf("expected title 'Video Title', got %s", item.Title)
	}
	if item.Duration != 180 {
		t.Errorf("expected duration 180, got %d", item.Duration)
	}
	if item.ThumbnailURL != "http://thumb.jpg" {
		t.Errorf("expected thumbnail 'http://thumb.jpg', got %s", item.ThumbnailURL)
	}

	// Set partial metadata (only title)
	err = reg.SetMeta("test-id", "New Title", 0, "")
	if err != nil {
		t.Fatalf("failed to set metadata: %v", err)
	}

	item = reg.Get("test-id")
	if item.Title != "New Title" {
		t.Errorf("expected title 'New Title', got %s", item.Title)
	}
	// Duration should remain unchanged
	if item.Duration != 180 {
		t.Errorf("expected duration 180 (unchanged), got %d", item.Duration)
	}
}

func TestItemRegistry_Delete(t *testing.T) {
	reg := NewItemRegistry(10)
	reg.Create("test-id", "http://example.com")

	// Delete existing item
	deleted := reg.Delete("test-id")
	if !deleted {
		t.Error("expected Delete to return true for existing item")
	}

	// Verify item is gone
	item := reg.Get("test-id")
	if item != nil {
		t.Error("expected item to be deleted")
	}

	// Delete non-existent item
	deleted = reg.Delete("missing")
	if deleted {
		t.Error("expected Delete to return false for non-existent item")
	}
}

func TestItemRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewItemRegistry(100)

	// Create initial items
	for i := 0; i < 10; i++ {
		id := genID()
		reg.Create(id, "http://example.com")
	}

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				id := genID()
				reg.Create(id, "http://example.com")
				reg.SetProgress(id, float64(j*10))
				reg.SetState(id, StateDownloading, "")
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				items := reg.Snapshot("")
				// Just access the data to ensure no race
				_ = len(items)
			}
		}()
	}

	wg.Wait()

	// Verify we have items
	items := reg.Snapshot("")
	if len(items) < 10 {
		t.Errorf("expected at least 10 items, got %d", len(items))
	}
}
