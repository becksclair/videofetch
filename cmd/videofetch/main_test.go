package main

import (
	"runtime"
	"testing"
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