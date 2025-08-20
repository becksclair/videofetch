package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"videofetch/internal/download"
)

// mock manager implementing downloadManager
type mockMgr struct {
	enqueueFn  func(url string) (string, error)
	snapshotFn func(id string) []*download.Item
}

func (m *mockMgr) Enqueue(url string) (string, error)  { return m.enqueueFn(url) }
func (m *mockMgr) Snapshot(id string) []*download.Item { return m.snapshotFn(id) }

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
	})
	w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.1", map[string]string{"url": "https://example.com/video"})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%s", ct)
	}
	var resp struct{ Status, Message, ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "success" || resp.ID != "abc123" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestDownloadSingle_MethodNotAllowed(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
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
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
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
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
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

func TestDownloadSingle_ErrorMappings(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"queue_full", errString("queue_full"), http.StatusTooManyRequests, "queue_full"},
		{"shutting_down", errString("shutting_down"), http.StatusServiceUnavailable, "shutting_down"},
		{"internal", errString("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", tc.err }, snapshotFn: func(id string) []*download.Item { return nil }})
			w := doJSON(t, h, http.MethodPost, "/api/download_single", "10.0.0.5", map[string]string{"url": "https://ok"})
			if w.Code != tc.code {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			var resp map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			if resp["message"] != tc.msg {
				t.Fatalf("msg=%v", resp["message"])
			}
		})
	}
}

func TestBatch_Success_Mixed(t *testing.T) {
	calls := 0
	h := New(&mockMgr{
		enqueueFn: func(url string) (string, error) {
			calls++
			if strings.Contains(url, "a") {
				return "id-a", nil
			}
			if strings.Contains(url, "b") {
				return "", errString("queue_full")
			}
			return "", nil
		},
		snapshotFn: func(id string) []*download.Item { return nil },
	})
	body := map[string]any{"urls": []string{"https://a", "invalid://x", "https://b"}}
	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.6", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Status, Message string
		IDs             []string `json:"ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "success" || len(resp.IDs) != 1 || resp.IDs[0] != "id-a" {
		t.Fatalf("resp=%+v", resp)
	}
	if calls != 2 {
		t.Fatalf("expected 2 enqueue calls (valid URLs), got %d", calls)
	}
}

func TestBatch_NoValidURLs(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
	body := map[string]any{"urls": []string{"notaurl", "ftp://x"}}
	w := doJSON(t, h, http.MethodPost, "/api/download", "10.0.0.7", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestBatch_InvalidJSON(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
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
	})
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
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
	w := doJSON(t, h, http.MethodPost, "/api/status", "10.0.0.10", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestRateLimiting_Exceeds(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "x", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
	ip := "203.0.113.1"
	var last *httptest.ResponseRecorder
	for i := 0; i < 61; i++ {
		last = doJSON(t, h, http.MethodGet, "/api/status", ip, nil)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", last.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(last.Body.Bytes(), &resp)
	if resp["message"] != "rate_limited" {
		t.Fatalf("resp=%v", resp)
	}
}

func TestHealthz_OK(t *testing.T) {
	h := New(&mockMgr{enqueueFn: func(url string) (string, error) { return "", nil }, snapshotFn: func(id string) []*download.Item { return nil }})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz unexpected: code=%d body=%q", w.Code, w.Body.String())
	}
}

// errString adapts a string to error without fmt.Errorf noise
type errString string

func (e errString) Error() string { return string(e) }
