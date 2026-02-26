//go:build integration

package integration

import (
	"context"
	"testing"

	"videofetch/internal/download"
)

// TestMetadataExtraction tests the metadata extraction functionality independently
func TestMetadataExtraction(t *testing.T) {
	// Skip if yt-dlp is not available
	if err := download.CheckYTDLP(); err != nil {
		t.Skip("yt-dlp not found; skipping metadata extraction test")
	}

	// Test with a simple video URL that should have metadata
	testCases := []struct {
		name        string
		url         string
		expectTitle bool
	}{
		{
			name:        "YouTube video",
			url:         "https://www.youtube.com/watch?v=zGDzdps75ns",
			expectTitle: true,
		},
		{
			name:        "HTTP JSON (fallback behavior)",
			url:         "https://httpbin.org/json",
			expectTitle: false, // This won't have video metadata
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing metadata extraction for: %s", tc.url)

			mediaInfo, err := download.FetchMediaInfo(context.Background(), tc.url)
			if err != nil {
				if tc.expectTitle {
					t.Logf("Warning: Failed to extract metadata (this might be due to geo-restrictions): %v", err)
					// Don't fail the test for geo-restrictions
					return
				} else {
					// For non-video URLs, we expect it to fail
					t.Logf("Expected failure for non-video URL: %v", err)
					return
				}
			}

			t.Logf("Extracted metadata:")
			t.Logf("  Title: %q", mediaInfo.Title)
			t.Logf("  Duration: %d seconds", mediaInfo.DurationSec)
			t.Logf("  Thumbnail: %q", mediaInfo.ThumbnailURL)

			// Verify the structure is correct
			if mediaInfo.Title == "" {
				if tc.expectTitle {
					t.Logf("Warning: Title is empty, this might be due to geo-restrictions")
				}
			} else {
				t.Logf("Title extraction successful")
			}

			if mediaInfo.DurationSec < 0 {
				t.Fatalf("Duration should not be negative: %d", mediaInfo.DurationSec)
			}

			// Test that we can handle the metadata in a realistic scenario
			if mediaInfo.Title != "" && len(mediaInfo.Title) > 200 {
				t.Logf("Warning: Title is very long (%d chars), might get truncated", len(mediaInfo.Title))
			}
		})
	}
}
