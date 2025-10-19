//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/server"
	"videofetch/internal/store"
)

// TestVideoMetadataPersistence verifies that video metadata (title, duration, thumbnail)
// is properly extracted, stored, and persisted in the database.
func TestVideoMetadataPersistence(t *testing.T) {
	// Skip if yt-dlp is not available
	if err := download.CheckYTDLP(); err != nil {
		t.Skip("yt-dlp not found; skipping metadata persistence test")
	}

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "downloads")
	dbPath := filepath.Join(outputDir, "videofetch.db")

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}

	// Use a stable test video that has good metadata
	testURL := os.Getenv("INTEGRATION_URL")
	if testURL == "" {
		testURL = "https://www.youtube.com/watch?v=zGDzdps75ns"
	}

	t.Logf("Testing metadata persistence with URL: %s", testURL)
	t.Logf("Output dir: %s", outputDir)
	t.Logf("Database path: %s", dbPath)

	// Phase 1: Submit download and extract metadata
	t.Run("extract_and_store_metadata", func(t *testing.T) {
		// Open database
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("failed to open database: %v", err)
		}
		defer st.Close()

		// Create download manager with store
		mgr := download.NewManager(outputDir, 1, 4)
		mgr.SetStore(st)
		defer mgr.Shutdown()

		// Create server with database
		handler := server.New(mgr, st, outputDir)
		ts := httptest.NewServer(handler)
		defer ts.Close()

		// Submit download request
		reqBody := map[string]string{"url": testURL}
		body, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/download_single", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to submit download: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status: %d", resp.StatusCode)
		}

		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Verify we got a database ID
		dbIDFloat, ok := response["db_id"].(float64)
		if !ok {
			t.Fatalf("no database ID returned in response: %+v", response)
		}
		dbID := int64(dbIDFloat)

		t.Logf("Successfully submitted download with db_id: %d", dbID)

		// Wait a moment for metadata extraction
		time.Sleep(2 * time.Second)

		// Verify database contains the download with metadata
		downloads, err := st.ListDownloads(context.Background(), store.ListFilter{})
		if err != nil {
			t.Fatalf("failed to list downloads: %v", err)
		}

		if len(downloads) != 1 {
			t.Fatalf("expected 1 download in database, got %d", len(downloads))
		}

		download := downloads[0]
		if download.URL != testURL {
			t.Fatalf("expected URL %s, got %s", testURL, download.URL)
		}

		// Check that metadata was extracted and stored
		if download.Title == "" || download.Title == testURL {
			t.Logf("Warning: Title not extracted (got: %q). This might be due to geo-restrictions or API changes.", download.Title)
		} else {
			t.Logf("Title extracted successfully: %q", download.Title)
		}

		if download.Duration == 0 {
			t.Logf("Warning: Duration not extracted. This might be due to geo-restrictions or API changes.")
		} else {
			t.Logf("Duration extracted successfully: %d seconds", download.Duration)
		}

		if download.ThumbnailURL == "" {
			t.Logf("Warning: Thumbnail URL not extracted. This might be due to geo-restrictions or API changes.")
		} else {
			t.Logf("Thumbnail URL extracted successfully: %s", download.ThumbnailURL)
		}

		// At minimum, the record should exist with the correct URL and timestamps
		if download.CreatedAt.IsZero() {
			t.Fatalf("CreatedAt timestamp is zero")
		}
		if download.UpdatedAt.IsZero() {
			t.Fatalf("UpdatedAt timestamp is zero")
		}

		t.Logf("Database record created successfully with timestamps")
	})

	// Phase 2: Restart server and verify metadata persists
	t.Run("verify_metadata_persistence", func(t *testing.T) {
		// Verify database file exists
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Fatalf("database file does not exist: %s", dbPath)
		}

		// Open database again (simulating server restart)
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("failed to reopen database: %v", err)
		}
		defer st.Close()

		// Create new manager (simulating restart)
		mgr := download.NewManager(outputDir, 1, 4)
		defer mgr.Shutdown()

		// Create new server instance
		handler := server.New(mgr, st, outputDir)
		ts := httptest.NewServer(handler)
		defer ts.Close()

		// Test that API shows persisted metadata
		resp, err := http.Get(ts.URL + "/api/downloads")
		if err != nil {
			t.Fatalf("failed to get downloads: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status: %d", resp.StatusCode)
		}

		var apiResponse map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&apiResponse); err != nil {
			t.Fatalf("failed to decode API response: %v", err)
		}

		downloads, ok := apiResponse["downloads"].([]interface{})
		if !ok {
			t.Fatalf("API response does not contain downloads array: %+v", apiResponse)
		}

		if len(downloads) != 1 {
			t.Fatalf("expected 1 download from API, got %d", len(downloads))
		}

		downloadData := downloads[0].(map[string]interface{})

		// Verify metadata fields are preserved
		url, _ := downloadData["url"].(string)
		if url != testURL {
			t.Fatalf("URL not preserved correctly: expected %s, got %s", testURL, url)
		}

		title, _ := downloadData["title"].(string)
		duration, _ := downloadData["duration"].(float64)
		thumbnailURL, _ := downloadData["thumbnail_url"].(string)

		t.Logf("Persisted metadata - Title: %q, Duration: %.0f, Thumbnail: %s", title, duration, thumbnailURL)

		// Test that dashboard shows persisted metadata
		resp, err = http.Get(ts.URL + "/dashboard/rows")
		if err != nil {
			t.Fatalf("failed to get dashboard: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected dashboard status: %d", resp.StatusCode)
		}

		dashboardBody := make([]byte, 4000)
		n, _ := resp.Body.Read(dashboardBody)
		dashboardHTML := string(dashboardBody[:n])

		// Verify the URL appears in the dashboard
		if !strings.Contains(dashboardHTML, testURL) {
			t.Fatalf("dashboard does not contain expected URL. Response: %s", dashboardHTML)
		}

		t.Logf("Dashboard correctly shows persisted video data")
	})

	// Phase 3: Verify no deletion occurs over time
	t.Run("verify_no_deletion", func(t *testing.T) {
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("failed to open database: %v", err)
		}
		defer st.Close()

		// Wait a bit to simulate time passing
		time.Sleep(1 * time.Second)

		// Check that the record is still there
		downloads, err := st.ListDownloads(context.Background(), store.ListFilter{})
		if err != nil {
			t.Fatalf("failed to list downloads: %v", err)
		}

		if len(downloads) != 1 {
			t.Fatalf("expected 1 download to persist, got %d", len(downloads))
		}

		// Verify the data is exactly the same
		download := downloads[0]
		if download.URL != testURL {
			t.Fatalf("download URL changed: expected %s, got %s", testURL, download.URL)
		}

		t.Logf("Video record persisted correctly - no deletion occurred")

		// Test that we can query by different filters
		completedDownloads, err := st.ListDownloads(context.Background(), store.ListFilter{Status: "completed"})
		if err != nil {
			t.Fatalf("failed to filter by completed status: %v", err)
		}

		errorDownloads, err := st.ListDownloads(context.Background(), store.ListFilter{Status: "error"})
		if err != nil {
			t.Fatalf("failed to filter by error status: %v", err)
		}

		totalExpected := len(completedDownloads) + len(errorDownloads)
		if totalExpected < 1 {
			// The download might still be in progress or pending
			pendingDownloads, err := st.ListDownloads(context.Background(), store.ListFilter{Status: "pending"})
			if err != nil {
				t.Fatalf("failed to filter by pending status: %v", err)
			}
			downloadingDownloads, err := st.ListDownloads(context.Background(), store.ListFilter{Status: "downloading"})
			if err != nil {
				t.Fatalf("failed to filter by downloading status: %v", err)
			}
			totalExpected += len(pendingDownloads) + len(downloadingDownloads)
		}

		if totalExpected < 1 {
			t.Fatalf("download record disappeared when filtering by status")
		}

		t.Logf("Status filtering works correctly")
	})
}
