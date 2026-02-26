package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"videofetch/internal/download"
	"videofetch/internal/logging"
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

func TestDownloadSingle_CreateFailureReturnsError(t *testing.T) {
	testStore := setupTestServerStore(t)
	_ = testStore.Close()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "unused", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, "/tmp/test")

	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.12", map[string]string{"url": "https://example.com/video"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["status"] != "error" || resp["message"] != "internal_error" {
		t.Fatalf("unexpected response: %+v", resp)
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

func TestBatch_AllValidButCreateFails(t *testing.T) {
	testStore := setupTestServerStore(t)
	_ = testStore.Close()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "unused", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, "/tmp/test")

	body := map[string]any{"urls": []string{"https://example.com/a", "https://example.com/b"}}
	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.13", body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["status"] != "error" || resp["message"] != "internal_error" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestBuildBatchResponse_DuplicatesAndCreateFailures_NoSuccess(t *testing.T) {
	code, resp := buildBatchResponse(3, nil, 2, 1)
	if code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", code)
	}
	if resp["status"] != "error" || resp["message"] != "internal_error" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp["failed"] != 1 {
		t.Fatalf("expected failed=1, got %+v", resp["failed"])
	}
	if resp["duplicates_skipped"] != 2 {
		t.Fatalf("expected duplicates_skipped=2, got %+v", resp["duplicates_skipped"])
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

	var resp struct {
		Status         string `json:"status"`
		Message        string `json:"message"`
		ExistingID     int64  `json:"existing_id"`
		ExistingStatus string `json:"existing_status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Status != "success" || resp.Message != "already_exists" {
		t.Fatalf("expected already_exists response, got: %+v", resp)
	}
	if resp.ExistingStatus != "completed" {
		t.Fatalf("expected existing_status completed, got %+v", resp)
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

func TestDownloadFile_UsesDirectLookupAndServesFile(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/watch?v=abc", "video", 30, "", "completed", 100)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	if err := testStore.UpdateFilename(ctx, id, "video.mp4"); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}
	if err := os.WriteFile(outputDir+"/video.mp4", []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, outputDir)

	req := httptest.NewRequest(http.MethodGet, "/api/download_file?id="+fmt.Sprintf("%d", id), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), `filename="video.mp4"`) {
		t.Fatalf("expected Content-Disposition filename, got %q", w.Header().Get("Content-Disposition"))
	}
}

func TestDownloadFile_NotFoundWhenRecordMissing(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/download_file?id=999999", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestAPIDownloadsDebugSummary_DoesNotLogRawPayloadByDefault(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	_, err := testStore.CreateDownload(ctx, "https://example.com/watch?v=1&token=supersecret", "video", 10, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prevDefault := slog.Default()
	prevLogger := logging.Logger
	slog.SetDefault(testLogger)
	logging.Logger = testLogger
	defer func() {
		slog.SetDefault(prevDefault)
		logging.Logger = prevLogger
	}()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir(), Options{UnsafeLogPayloads: false})

	req := httptest.NewRequest(http.MethodGet, "/api/downloads", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	logOut := buf.String()
	if strings.Contains(logOut, "supersecret") {
		t.Fatalf("expected no raw payload secrets in logs, got: %s", logOut)
	}
	if !strings.Contains(logOut, "\"event\":\"api_response_summary\"") {
		t.Fatalf("expected api_response_summary debug log, got: %s", logOut)
	}
}

func TestAPIDownloadsDebugSummary_UnsafeFlagAllowsRawPayload(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	_, err := testStore.CreateDownload(ctx, "https://example.com/watch?v=1&token=supersecret", "video", 10, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prevDefault := slog.Default()
	prevLogger := logging.Logger
	slog.SetDefault(testLogger)
	logging.Logger = testLogger
	defer func() {
		slog.SetDefault(prevDefault)
		logging.Logger = prevLogger
	}()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir(), Options{UnsafeLogPayloads: true})

	req := httptest.NewRequest(http.MethodGet, "/api/downloads", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	logOut := buf.String()
	if !strings.Contains(logOut, "\"event\":\"api_response_raw\"") {
		t.Fatalf("expected api_response_raw debug log, got: %s", logOut)
	}
	if !strings.Contains(logOut, "supersecret") {
		t.Fatalf("expected unsafe raw payload to include token in logs when enabled")
	}
}

func TestLoggerMiddleware_RecordsRealStatusAndBytes(t *testing.T) {
	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prevDefault := slog.Default()
	prevLogger := logging.Logger
	slog.SetDefault(testLogger)
	logging.Logger = testLogger
	defer func() {
		slog.SetDefault(prevDefault)
		logging.Logger = prevLogger
	}()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 from unknown route, got %d", w.Code)
	}

	status, bytes, ok := latestHTTPAccessLog(t, buf.String())
	if !ok {
		t.Fatalf("expected http_request log entry, got logs: %s", buf.String())
	}
	if status != http.StatusNotFound {
		t.Fatalf("expected logged status 404, got %d", status)
	}
	if bytes <= 0 {
		t.Fatalf("expected positive response_bytes, got %d", bytes)
	}
}

func TestDashboardRows_UsesPersistedErrorMessageSnippetAndTooltip(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	longErr := strings.Repeat("x", 130)
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 10, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	if err := testStore.UpdateStatus(ctx, id, "error", longErr); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/dashboard/rows", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	snippet := strings.Repeat("x", 120) + "…"
	if !strings.Contains(body, snippet) {
		t.Fatalf("expected truncated error snippet in body")
	}
	if !strings.Contains(body, `title="`+longErr+`"`) {
		t.Fatalf("expected full error in tooltip title, body=%q", body)
	}
}

func TestAPIDownloads_IncludesPersistedErrorMessage(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 10, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	if err := testStore.UpdateStatus(ctx, id, "error", "enqueue_failed: boom"); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/downloads", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Status    string           `json:"status"`
		Downloads []store.Download `json:"downloads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if len(resp.Downloads) != 1 {
		t.Fatalf("expected one download, got %d", len(resp.Downloads))
	}
	if resp.Downloads[0].ErrorMessage != "enqueue_failed: boom" {
		t.Fatalf("expected error_message to be present, got %q", resp.Downloads[0].ErrorMessage)
	}
}

func TestAPIDownloads_LimitAndOffset(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	_, _ = testStore.CreateDownload(ctx, "https://example.com/1", "One", 0, "", "pending", 0)
	_, _ = testStore.CreateDownload(ctx, "https://example.com/2", "Two", 0, "", "pending", 0)
	_, _ = testStore.CreateDownload(ctx, "https://example.com/3", "Three", 0, "", "pending", 0)

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/downloads?sort=created_at&order=asc&limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Status    string           `json:"status"`
		Downloads []store.Download `json:"downloads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if len(resp.Downloads) != 2 {
		t.Fatalf("expected 2 downloads, got %d", len(resp.Downloads))
	}
	if !strings.Contains(resp.Downloads[0].URL, "/2") || !strings.Contains(resp.Downloads[1].URL, "/3") {
		t.Fatalf("unexpected pagination order: %+v", resp.Downloads)
	}
}

func TestControlEndpoints_PauseResumeCancel(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		pauseFn:    func(dbID int64) bool { return true },
		cancelFn:   func(dbID int64) bool { return true },
		resumeFn:   func(dbID int64) (bool, error) { return false, nil },
	}, testStore, t.TempDir())

	pauseResp := doJSON(t, h, http.MethodPost, "/api/control/pause", "", map[string]any{"id": id})
	if pauseResp.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", pauseResp.Code, pauseResp.Body.String())
	}
	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() after pause failed: found=%v err=%v", found, err)
	}
	if row.Status != "paused" {
		t.Fatalf("expected paused status, got %q", row.Status)
	}

	resumeResp := doJSON(t, h, http.MethodPost, "/api/control/resume", "", map[string]any{"id": id})
	if resumeResp.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resumeResp.Code, resumeResp.Body.String())
	}
	row, found, err = testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() after resume failed: found=%v err=%v", found, err)
	}
	if row.Status != "pending" {
		t.Fatalf("expected pending after resume, got %q", row.Status)
	}

	cancelResp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if cancelResp.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelResp.Code, cancelResp.Body.String())
	}
	row, found, err = testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() after cancel failed: found=%v err=%v", found, err)
	}
	if row.Status != "canceled" {
		t.Fatalf("expected canceled after cancel, got %q", row.Status)
	}
}

func TestControlResume_InMemoryResumeDoesNotPersistPending(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "paused", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		resumeFn:   func(dbID int64) (bool, error) { return true, nil },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/resume", "", map[string]any{"id": id})
	if resp.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "downloading" {
		t.Fatalf("expected in-memory resumed row to persist as downloading, got %q", row.Status)
	}
}

func TestControlResume_ReturnsShuttingDown(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "paused", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		resumeFn:   func(dbID int64) (bool, error) { return false, download.ErrShuttingDown },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/resume", "", map[string]any{"id": id})
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() failed: %v", err)
	}
	if payload["message"] != "shutting_down" {
		t.Fatalf("expected shutting_down message, got %+v", payload)
	}
}

func TestControlPlay_NotPlayableUnlessCompleted(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/play", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-completed row, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestCancelControl_RemovesTrackedArtifacts(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	tracked := []string{
		fmt.Sprintf("%s/%s", outputDir, "partial.part"),
	}
	if err := testStore.UpdateArtifacts(ctx, id, tracked); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}
	if err := os.WriteFile(tracked[0], []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		cancelFn:   func(dbID int64) bool { return false },
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if resp.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", resp.Code, resp.Body.String())
	}
	if _, err := os.Stat(tracked[0]); !os.IsNotExist(err) {
		t.Fatalf("expected tracked artifact to be removed, err=%v", err)
	}
}

func TestDeleteEndpoint_CompletedRemovesFilesAndRow(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "completed", 100)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	filename := "video.mp4"
	if err := testStore.UpdateFilename(ctx, id, filename); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}
	outputPath := filepath.Join(outputDir, filename)
	if err := os.WriteFile(outputPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(output) failed: %v", err)
	}

	tracked := []string{
		filepath.Join(outputDir, "video.part"),
	}
	if err := testStore.UpdateArtifacts(ctx, id, tracked); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}
	if err := os.WriteFile(tracked[0], []byte("part"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked) failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodDelete, "/api/delete", "", map[string]any{"id": id})
	if resp.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", resp.Code, resp.Body.String())
	}

	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("expected output file to be removed, err=%v", err)
	}
	if _, err := os.Stat(tracked[0]); !os.IsNotExist(err) {
		t.Fatalf("expected tracked artifact to be removed, err=%v", err)
	}
	if _, found, err := testStore.GetDownloadByID(ctx, id); err != nil || found {
		t.Fatalf("expected row deleted, found=%v err=%v", found, err)
	}
}

func TestDeleteEndpoint_InvalidStateRejected(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "canceled", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodDelete, "/api/delete", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("expected row to remain, found=%v err=%v", found, err)
	}
	if row.Status != "canceled" {
		t.Fatalf("expected status to remain canceled, got %q", row.Status)
	}
}

func TestDeleteEndpoint_FileDeleteFailureKeepsRow(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "completed", 100)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	stubbornDir := filepath.Join(outputDir, "stubborn")
	if err := os.MkdirAll(filepath.Join(stubbornDir, "child"), 0o755); err != nil {
		t.Fatalf("MkdirAll(stubborn) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stubbornDir, "child", "x.part"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(stubborn child) failed: %v", err)
	}
	if err := testStore.UpdateArtifacts(ctx, id, []string{stubbornDir}); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodDelete, "/api/delete", "", map[string]any{"id": id})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() failed: %v", err)
	}
	if payload["message"] != "delete_failed" {
		t.Fatalf("expected delete_failed message, got %+v", payload)
	}

	if _, found, err := testStore.GetDownloadByID(ctx, id); err != nil || !found {
		t.Fatalf("expected row to remain after delete failure, found=%v err=%v", found, err)
	}
}

func TestDeleteEndpoint_MissingFileStillDeletesRow(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "completed", 100)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	if err := testStore.UpdateFilename(ctx, id, "missing.mp4"); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodDelete, "/api/delete", "", map[string]any{"id": id})
	if resp.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", resp.Code, resp.Body.String())
	}
	if _, found, err := testStore.GetDownloadByID(ctx, id); err != nil || found {
		t.Fatalf("expected row deleted, found=%v err=%v", found, err)
	}
}

func TestDeleteEndpoint_NotFound(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodDelete, "/api/delete", "", map[string]any{"id": 999999})
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestCancelControl_DoesNotOverrideConcurrentCompletion(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	tracked := []string{
		fmt.Sprintf("%s/%s", outputDir, "partial.part"),
	}
	if err := testStore.UpdateArtifacts(ctx, id, tracked); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}
	if err := os.WriteFile(tracked[0], []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked) failed: %v", err)
	}

	filename := "video.mp4"
	if err := testStore.UpdateFilename(ctx, id, filename); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}
	outputPath := filepath.Join(outputDir, filename)
	if err := os.WriteFile(outputPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(output) failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		cancelFn: func(dbID int64) bool {
			_ = testStore.UpdateStatus(context.Background(), dbID, "completed", "")
			return true
		},
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when download completed concurrently, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "completed" {
		t.Fatalf("expected status to remain completed, got %q", row.Status)
	}
	if _, err := os.Stat(tracked[0]); err != nil {
		t.Fatalf("expected tracked file to remain, err=%v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected completed file to remain, err=%v", err)
	}
}

func TestPauseControl_DownloadingRequiresManagerAck(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "downloading", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		pauseFn:    func(dbID int64) bool { return false },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/pause", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when manager cannot pause active download, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "downloading" {
		t.Fatalf("expected status to remain downloading, got %q", row.Status)
	}
}

func TestCancelControl_DownloadingRequiresManagerAck(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "downloading", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		cancelFn:   func(dbID int64) bool { return false },
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when manager cannot cancel active download, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "downloading" {
		t.Fatalf("expected status to remain downloading, got %q", row.Status)
	}
}

func TestPauseControl_PendingBecomesDownloadingRequiresManagerAck(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		pauseFn: func(dbID int64) bool {
			_ = testStore.UpdateStatus(context.Background(), dbID, "downloading", "")
			return false
		},
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/pause", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when pending row transitions active without manager ack, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "downloading" {
		t.Fatalf("expected status to remain downloading, got %q", row.Status)
	}
}

func TestCancelControl_PendingBecomesDownloadingRequiresManagerAck(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		cancelFn: func(dbID int64) bool {
			_ = testStore.UpdateStatus(context.Background(), dbID, "downloading", "")
			return false
		},
	}, testStore, t.TempDir())

	resp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when pending row transitions active without manager ack, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "downloading" {
		t.Fatalf("expected status to remain downloading, got %q", row.Status)
	}
}

func TestCancelControl_StopRequestedUsesStabilizationEvenWhenStatusPending(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	outputDir := t.TempDir()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}
	tracked := []string{
		fmt.Sprintf("%s/%s", outputDir, "partial.part"),
	}
	if err := testStore.UpdateArtifacts(ctx, id, tracked); err != nil {
		t.Fatalf("UpdateArtifacts() failed: %v", err)
	}
	if err := os.WriteFile(tracked[0], []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked) failed: %v", err)
	}
	filename := "video.mp4"
	if err := testStore.UpdateFilename(ctx, id, filename); err != nil {
		t.Fatalf("UpdateFilename() failed: %v", err)
	}
	outputPath := filepath.Join(outputDir, filename)
	if err := os.WriteFile(outputPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(output) failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
		cancelFn: func(dbID int64) bool {
			go func() {
				time.Sleep(20 * time.Millisecond)
				_ = testStore.UpdateStatus(context.Background(), dbID, "completed", "")
			}()
			return true
		},
	}, testStore, outputDir)

	resp := doJSON(t, h, http.MethodPost, "/api/control/cancel", "", map[string]any{"id": id})
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when status flips to completed during stabilization, got %d body=%s", resp.Code, resp.Body.String())
	}

	row, found, err := testStore.GetDownloadByID(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetDownloadByID() failed: found=%v err=%v", found, err)
	}
	if row.Status != "completed" {
		t.Fatalf("expected completed status to persist, got %q", row.Status)
	}
	if _, err := os.Stat(tracked[0]); err != nil {
		t.Fatalf("expected tracked artifact to remain, err=%v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected output file to remain, err=%v", err)
	}
}

func TestWaitForStableCanceled_FlipsToCompleted(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()

	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "canceled", 42)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = testStore.UpdateStatus(context.Background(), id, "completed", "")
	}()

	row, stable, err := waitForStableCanceled(ctx, testStore, id, 180*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForStableCanceled() failed: %v", err)
	}
	if stable {
		t.Fatalf("expected canceled state to be unstable")
	}
	if row.Status != "completed" {
		t.Fatalf("expected final observed status completed, got %q", row.Status)
	}
}

func TestWebsocketDownloads_DiffEvents(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())
	srv := httptest.NewServer(h)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws/downloads?limit=50"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))

	var first map[string]any
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read snapshot failed: %v", err)
	}
	if first["type"] != "snapshot" {
		t.Fatalf("expected initial snapshot, got %+v", first)
	}

	if err := testStore.UpdateStatus(ctx, id, "downloading", ""); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}

	foundDiff := false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read ws message failed: %v", err)
		}
		if msg["type"] != "diff" {
			continue
		}
		upserts, _ := msg["upserts"].([]any)
		if len(upserts) > 0 {
			foundDiff = true
			break
		}
	}
	if !foundDiff {
		t.Fatalf("expected to receive diff upsert event")
	}
}

func TestWebsocketDownloads_CoalescesBurstChanges(t *testing.T) {
	testStore := setupTestServerStore(t)
	defer testStore.Close()
	ctx := context.Background()
	id, err := testStore.CreateDownload(ctx, "https://example.com/video", "Video", 0, "", "pending", 0)
	if err != nil {
		t.Fatalf("CreateDownload() failed: %v", err)
	}

	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, testStore, t.TempDir())
	srv := httptest.NewServer(h)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws/downloads?limit=50"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var snapshot map[string]any
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read snapshot failed: %v", err)
	}
	if snapshot["type"] != "snapshot" {
		t.Fatalf("expected initial snapshot, got %+v", snapshot)
	}

	// Two rapid updates should be coalesced into a single diff frame.
	if err := testStore.UpdateProgress(ctx, id, 10); err != nil {
		t.Fatalf("UpdateProgress(10) failed: %v", err)
	}
	if err := testStore.UpdateProgress(ctx, id, 25); err != nil {
		t.Fatalf("UpdateProgress(25) failed: %v", err)
	}

	diffCount := 0
	lastProgress := -1.0
	end := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(end) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			t.Fatalf("read ws message failed: %v", err)
		}
		if msg["type"] != "diff" {
			continue
		}
		upserts, _ := msg["upserts"].([]any)
		if len(upserts) == 0 {
			continue
		}
		diffCount++
		first, _ := upserts[0].(map[string]any)
		if progress, ok := first["progress"].(float64); ok {
			lastProgress = progress
		}
	}

	if diffCount != 1 {
		t.Fatalf("expected exactly one coalesced diff message, got %d", diffCount)
	}
	if lastProgress != 25 {
		t.Fatalf("expected latest coalesced progress 25, got %.1f", lastProgress)
	}
}

func TestIsAllowedWebSocketOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/ws/downloads", nil)

	req.Header.Set("Origin", "http://127.0.0.1:8080")
	if !isAllowedWebSocketOrigin(req) {
		t.Fatalf("expected same-origin request to be allowed")
	}

	req.Header.Set("Origin", "https://evil.example")
	if isAllowedWebSocketOrigin(req) {
		t.Fatalf("expected cross-origin request to be rejected")
	}

	req.Header.Set("Origin", "chrome-extension://abcdefghijklmnopabcdefghijklmnop")
	if !isAllowedWebSocketOrigin(req) {
		t.Fatalf("expected chrome extension origin to be allowed")
	}

	req.Header.Set("Origin", "://bad-origin")
	if isAllowedWebSocketOrigin(req) {
		t.Fatalf("expected invalid origin header to be rejected")
	}
}

func latestHTTPAccessLog(t *testing.T, logs string) (status int, responseBytes int, ok bool) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row["event"] != "http_request" {
			continue
		}
		statusVal, sOK := row["status"].(float64)
		bytesVal, bOK := row["response_bytes"].(float64)
		if !sOK || !bOK {
			continue
		}
		return int(statusVal), int(bytesVal), true
	}
	return 0, 0, false
}

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
