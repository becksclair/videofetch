package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"videofetch/internal/download"
	"videofetch/internal/store"
)

// helpers
func doJSON(t *testing.T, h http.Handler, method, path, ip string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestDownloadSingle_Success(t *testing.T) {
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "abc123", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, nil, "/tmp/test")
	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.1", map[string]string{"url": "https://example.com/video"})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%s", ct)
	}
	var resp struct{ Status, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// With async pattern, no ID is returned immediately (it's processed in background)
	if resp.Status != "success" || resp.Message != "enqueued" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestDownloadSingle_MethodNotAllowed(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	w := doJSON(t, h, http.MethodGet, "/api/download_single", "10.0.0.2", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["message"] != "method_not_allowed" {
		t.Fatalf("resp=%v", resp)
	}
}

func TestDownloadSingle_InvalidJSON(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodPost, "/api/download_single", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.0.0.3")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestDownloadSingle_InvalidURL(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.4", map[string]string{"url": "ftp://example"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["message"] != "invalid_url" {
		t.Fatalf("resp=%v", resp)
	}
}

// TestDownloadSingle_AsyncPattern verifies async behavior without direct Enqueue calls
func TestDownloadSingle_AsyncPattern(t *testing.T) {
	// With async pattern, the handler returns success immediately
	// and doesn't call Enqueue directly (it's done via ProcessPendingDownload)
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "id", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, nil, "/tmp/test")
	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.5", map[string]string{"url": "https://ok"})
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "success" {
		t.Fatalf("expected success status, got %v", resp["status"])
	}
}

func TestBatch_Success_Mixed(t *testing.T) {
	// Note: /api/download now stores to DB and doesn't directly call Enqueue
	// Without a store, it returns success with empty db_ids
	h := New(&mockMgr{
		enqueueFn: func(url string) (string, error) {
			t.Fatal("should not call Enqueue in batch endpoint")
			return "", nil
		},
		snapshotFn: func(id string) []*download.Item { return nil },
	}, nil, "/tmp/test")
	body := map[string]any{"urls": []string{"https://a", "invalid://x", "https://b"}}
	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.6", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Status, Message string
		DBIDs           []int64 `json:"db_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Without a store, no DB IDs are returned
	if resp.Status != "success" || len(resp.DBIDs) != 0 {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestBatch_NoValidURLs(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	body := map[string]any{"urls": []string{"notaurl", "ftp://x"}}
	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.7", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestBatch_InvalidJSON(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodPost, "/api/download", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.0.0.11")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestStatus_AllAndByID(t *testing.T) {
	items := []*download.Item{{ID: "one", URL: "u1", Progress: 42, State: download.StateDownloading}, {ID: "two", URL: "u2", Progress: 100, State: download.StateCompleted}}
	h := New(&mockMgr{
		enqueueFn: func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item {
			if id == "" {
				return items
			}
			return []*download.Item{{ID: id, URL: "uX", Progress: 0, State: download.StateQueued}}
		},
	}, nil, "/tmp/test")
	// all
	w := doJSON(t, h, http.MethodGet, "/api/status", "10.0.0.8", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp struct {
		Status    string
		Downloads []download.Item
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Downloads) != 2 {
		t.Fatalf("downloads=%d", len(resp.Downloads))
	}
	// by id
	req := httptest.NewRequest(http.MethodGet, "/api/status?id=abc", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.9")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("status=%d", w2.Code)
	}
	var resp2 struct{ Downloads []download.Item }
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if len(resp2.Downloads) != 1 || resp2.Downloads[0].ID != "abc" {
		t.Fatalf("resp=%+v", resp2)
	}
}

func TestStatus_MethodNotAllowed(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	w := doJSON(t, h, http.MethodPost, "/api/status", "10.0.0.10", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestDashboardRows_FilterAndSort(t *testing.T) {
	items := []*download.Item{
		{ID: "a", URL: "https://a", State: download.StateDownloading, Progress: 20},
		{ID: "b", URL: "https://b", State: download.StateQueued, Progress: 50},
		{ID: "c", URL: "https://c", State: download.StateDownloading, Progress: 80},
	}
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return items },
	}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodGet, "/dashboard/rows?status=downloading&sort=progress&order=desc", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.50")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "queued") { // filtered out
		t.Fatalf("expected no queued items in filtered rows")
	}
	i80 := strings.Index(body, "data-progress=\"80.0\"")
	i20 := strings.Index(body, "data-progress=\"20.0\"")
	if i80 < 0 || i20 < 0 || !(i80 < i20) {
		t.Fatalf("expected 80%% progress row before 20%% (desc). body=%q", body)
	}
}

// Rate limiting has been removed - this test is now just checking that
// multiple requests succeed without rate limiting
func TestRateLimiting_Removed(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "x", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	ip := "203.0.113.1"
	var last *httptest.ResponseRecorder
	// All 61 requests should succeed now
	for i := 0; i < 61; i++ {
		last = doJSON(t, h, http.MethodGet, "/api/status", ip, nil)
		if last.Code != http.StatusOK {
			t.Fatalf("request %d failed with status %d", i, last.Code)
		}
	}
}

func TestHealthz_OK(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz unexpected: code=%d body=%q", w.Code, w.Body.String())
	}
}

func TestDownloadSingle_DuplicateCompleted(t *testing.T) {
	// Create test store with completed download
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	testURL := "https://already-completed.com/video"

	// Create a completed download in the store
	id, err := testStore.CreateDownload(ctx, testURL, "Test Video", 300, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	mgr := &mockMgr{
		enqueueFn:  func(url string) (string, error) { return "should-not-be-called", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}

	h := New(mgr, testStore, "/tmp/test")
	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.100",
		map[string]string{"url": testURL})

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct{ Status, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "success" || resp.Message != "already_completed" {
		t.Fatalf("expected already_completed response, got: %+v", resp)
	}
	_ = id // avoid unused variable warning
}

func TestBatch_DuplicateFiltering(t *testing.T) {
	// Create test store with some completed downloads
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()

	// Create completed downloads
	_, err := testStore.CreateDownload(ctx, "https://completed1.com/video", "Video 1", 300, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	_, err = testStore.CreateDownload(ctx, "https://completed2.com/video", "Video 2", 400, "", "completed", 100.0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	enqueueCalls := 0
	mgr := &mockMgr{
		enqueueFn: func(url string) (string, error) {
			enqueueCalls++
			return "new-id-" + string(rune('0'+enqueueCalls)), nil
		},
		snapshotFn: func(id string) []*download.Item { return nil },
	}

	h := New(mgr, testStore, "/tmp/test")

	body := map[string]any{
		"urls": []string{
			"https://completed1.com/video", // should be skipped
			"https://new1.com/video",       // should be enqueued
			"https://completed2.com/video", // should be skipped
			"https://new2.com/video",       // should be enqueued
			"invalid://url",                // should be skipped (invalid)
		},
	}

	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.101", body)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Status, Message   string
		DBIDs             []int64 `json:"db_ids"`
		DuplicatesSkipped int     `json:"duplicates_skipped,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "success" || len(resp.DBIDs) != 2 {
		t.Fatalf("expected 2 db_ids for new URLs, got: %+v", resp)
	}

	if resp.DuplicatesSkipped != 2 {
		t.Fatalf("expected 2 duplicates skipped, got %d", resp.DuplicatesSkipped)
	}

	// Note: Enqueue is no longer called directly from /api/download,
	// it's called via ProcessPendingDownload in a goroutine
	// So we shouldn't expect immediate enqueue calls here
}

// errString adapts a string to error without fmt.Errorf noise
type errString string

func (e errString) Error() string { return string(e) }

// setupTestServerStore creates an in-memory test store
func setupTestServerStore(t *testing.T) *store.Store {
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	testStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open test store: %v", err)
	}

	return testStore
}
