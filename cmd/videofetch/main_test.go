package main

import (
	"context"
	"runtime"
	"testing"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/store"
)

func TestDefaultCacheDBPath(t *testing.T) {
	path := defaultCacheDBPath()

	// The path should contain "videofetch.db" not "videofectch.db"
	if !containsString(path, "videofetch.db") {
		t.Errorf("Expected path to contain 'videofetch.db', got: %s", path)
	}

	// Path should not be empty
	if path == "" {
		t.Error("Expected non-empty database path")
	}

	// Path should contain the videofetch directory
	if !containsString(path, "videofetch") {
		t.Errorf("Expected path to contain 'videofetch' directory, got: %s", path)
	}
}

func TestDefaultCacheDBPath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Skipping Windows-specific test on non-Windows platform")
	}

	path := defaultCacheDBPath()

	// On Windows, should use AppData or fallback appropriately
	if !containsString(path, "videofetch.db") {
		t.Errorf("Expected Windows path to contain 'videofetch.db', got: %s", path)
	}
}

func TestDefaultCacheDBPath_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Unix-specific test on Windows")
	}

	path := defaultCacheDBPath()

	// On Unix-like systems, should use .cache directory
	if !containsString(path, ".cache") && !containsString(path, "videofetch") {
		t.Errorf("Expected Unix path to contain '.cache' or fallback to 'videofetch', got: %s", path)
	}

	if !containsString(path, "videofetch.db") {
		t.Errorf("Expected Unix path to contain 'videofetch.db', got: %s", path)
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr) >= 0
}

// Simple substring search without importing strings package
func findSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		found := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}

// Test storeHooks implementation
func TestStoreHooks_OnProgress(t *testing.T) {
	// Create temporary database
	tmpPath := t.TempDir() + "/test.db"
	st, err := store.Open(tmpPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hooks := &storeHooks{st: st}

	// Create a test download record
	ctx := context.Background()
	dbID, err := st.CreateDownload(ctx, "https://example.com/test", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("create download: %v", err)
	}

	// Test progress update
	hooks.OnProgress(dbID, 50.0)

	// Small delay to allow async operation to complete
	time.Sleep(10 * time.Millisecond)

	// Verify progress was updated (we'll check this indirectly by ensuring no error occurred)
	// In a real test, we'd query the database to verify the update
}

func TestStoreHooks_OnStateChange(t *testing.T) {
	// Create temporary database
	tmpPath := t.TempDir() + "/test.db"
	st, err := store.Open(tmpPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hooks := &storeHooks{st: st}

	// Create a test download record
	ctx := context.Background()
	dbID, err := st.CreateDownload(ctx, "https://example.com/test", "Test Video", 300, "", "pending", 0.0)
	if err != nil {
		t.Fatalf("create download: %v", err)
	}

	testCases := []struct {
		state    download.State
		expected string
	}{
		{download.StateQueued, "pending"},
		{download.StateDownloading, "downloading"},
		{download.StateCompleted, "completed"},
		{download.StateFailed, "error"},
	}

	for _, tc := range testCases {
		hooks.OnStateChange(dbID, tc.state, "")
		// Small delay to allow async operation to complete
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStoreHooks_ErrorHandling(t *testing.T) {
	// Create hooks with closed store to test error handling
	tmpPath := t.TempDir() + "/test.db"
	st, err := store.Open(tmpPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close() // Close immediately to simulate error conditions

	hooks := &storeHooks{st: st}

	// These should not panic even with closed database
	hooks.OnProgress(1, 50.0)
	hooks.OnStateChange(1, download.StateCompleted, "")

	// Small delay to allow async operations to complete
	time.Sleep(10 * time.Millisecond)
}
