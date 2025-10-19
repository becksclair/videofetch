package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"videofetch/internal/download"
)

func TestDashboard_HTML_OK(t *testing.T) {
	items := []*download.Item{{ID: "abcdef012345", URL: "https://x", Progress: 10, State: download.StateQueued}}
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return items },
	}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if ct := strings.ToLower(w.Header().Get("Content-Type")); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type=%s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "VideoFetch Dashboard") || !strings.Contains(body, "htmx.org") {
		t.Fatalf("html missing expected markers: %q", body)
	}
}

func TestDashboard_Rows_OK(t *testing.T) {
	items := []*download.Item{{ID: "id1", URL: "https://example.com/a", Progress: 42, State: download.StateDownloading}}
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "", nil },
		snapshotFn: func(id string) []*download.Item { return items },
	}, nil, "/tmp/test")
	req := httptest.NewRequest(http.MethodGet, "/dashboard/rows", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.2")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "table") || !strings.Contains(w.Body.String(), "downloading") {
		t.Fatalf("rows html unexpected: %q", w.Body.String())
	}
}

func TestDashboard_Enqueue_OK_And_Invalid(t *testing.T) {
	// With async pattern, Enqueue isn't called directly from the handler
	h := New(&mockMgr{
		enqueueFn:  func(url string) (string, error) { return "newid", nil },
		snapshotFn: func(id string) []*download.Item { return nil },
	}, nil, "/tmp/test")
	// Valid submission
	req := httptest.NewRequest(http.MethodPost, "/dashboard/enqueue", strings.NewReader("url=https%3A%2F%2Fok"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "198.51.100.3")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "✓ Video queued successfully") {
		t.Fatalf("expected success message in response: %q", w.Body.String())
	}
	// Note: Enqueue is no longer called directly in async pattern

	// Invalid URL
	req2 := httptest.NewRequest(http.MethodPost, "/dashboard/enqueue", strings.NewReader("url=ftp%3A%2F%2Fbad"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("X-Forwarded-For", "198.51.100.4")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w2.Code)
	}
}

// mock manager implementing downloadManager for dashboard tests
type mockMgr struct {
	enqueueFn  func(url string) (string, error)
	snapshotFn func(id string) []*download.Item
}

func (m *mockMgr) Enqueue(url string) (string, error)                            { return m.enqueueFn(url) }
func (m *mockMgr) Snapshot(id string) []*download.Item                           { return m.snapshotFn(id) }
func (m *mockMgr) AttachDB(id string, dbID int64)                                {}
func (m *mockMgr) SetMeta(id string, title string, duration int64, thumb string) {}
