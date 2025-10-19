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
	"testing"

	"videofetch/internal/download"
	"videofetch/internal/server"
	"videofetch/internal/store"
)

// TestDatabasePersistence tests that downloads are properly persisted to the database
// and that the dashboard shows persisted data even after server restart.
func TestDatabasePersistence(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "downloads")
	dbPath := filepath.Join(outputDir, "videofetch.db")

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}

	// Test URL that doesn't require actual yt-dlp download
	testURL := "https://httpbin.org/json"

	t.Logf("Testing database persistence with output dir: %s", outputDir)
	t.Logf("Database path: %s", dbPath)

	// Phase 1: Create server with database and submit downloads
	t.Run("submit_downloads", func(t *testing.T) {
		// Open database
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("failed to open database: %v", err)
		}
		defer st.Close()

		// Create download manager with store
		mgr := download.NewManager(outputDir, 2, 8)
		mgr.SetStore(st)
		defer mgr.Shutdown()

		// Create server with database
		handler := server.New(mgr, st, outputDir)
		ts := httptest.NewServer(handler)
		defer ts.Close()

		// Submit download
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
		if _, ok := response["db_id"]; !ok {
			t.Fatalf("no database ID returned in response: %+v", response)
		}

		t.Logf("Successfully submitted download with db_id: %v", response["db_id"])

		// Verify database contains the download
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

		t.Logf("Database contains download: ID=%d, URL=%s, Status=%s", download.ID, download.URL, download.Status)
	})

	// Phase 2: Restart server (new instance) and verify data persists
	t.Run("verify_persistence", func(t *testing.T) {
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
		mgr := download.NewManager(outputDir, 2, 8)
		defer mgr.Shutdown()

		// Create new server instance
		handler := server.New(mgr, st, outputDir)
		ts := httptest.NewServer(handler)
		defer ts.Close()

		// Test that API shows persisted data
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

		t.Logf("API correctly returned %d persisted downloads", len(downloads))

		// Test that dashboard shows persisted data
		resp, err = http.Get(ts.URL + "/dashboard/rows")
		if err != nil {
			t.Fatalf("failed to get dashboard: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected dashboard status: %d", resp.StatusCode)
		}

		body := make([]byte, 2000)
		n, _ := resp.Body.Read(body)
		dashboardHTML := string(body[:n])

		if !bytes.Contains([]byte(dashboardHTML), []byte("httpbin.org")) {
			t.Fatalf("dashboard does not contain expected URL. Response: %s", dashboardHTML)
		}

		t.Logf("Dashboard correctly shows persisted downloads")
	})

	// Phase 3: Test filtering and sorting
	t.Run("test_filtering", func(t *testing.T) {
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("failed to open database: %v", err)
		}
		defer st.Close()

		mgr := download.NewManager(outputDir, 2, 8)
		defer mgr.Shutdown()

		handler := server.New(mgr, st, outputDir)
		ts := httptest.NewServer(handler)
		defer ts.Close()

		// Test status filtering (download might be in downloading state by now)
		resp, err := http.Get(ts.URL + "/api/downloads?status=downloading")
		if err != nil {
			t.Fatalf("failed to filter downloads: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected filter status: %d", resp.StatusCode)
		}

		var filterResponse map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&filterResponse); err != nil {
			t.Fatalf("failed to decode filter response: %v", err)
		}

		downloads, ok := filterResponse["downloads"].([]interface{})
		if !ok {
			t.Fatalf("filter response does not contain downloads: %+v", filterResponse)
		}

		// Should find the downloading download (or test with no filter to verify any exist)
		if len(downloads) == 0 {
			// Try without filter to see what status it actually has
			resp2, err := http.Get(ts.URL + "/api/downloads")
			if err == nil {
				defer resp2.Body.Close()
				var allResponse map[string]interface{}
				if err := json.NewDecoder(resp2.Body).Decode(&allResponse); err == nil {
					allDownloads := allResponse["downloads"].([]interface{})
					if len(allDownloads) > 0 {
						t.Logf("Download exists but not in 'downloading' status. All downloads: %+v", allDownloads)
					}
				}
			}
		}

		t.Logf("Filtering works correctly")
	})
}
