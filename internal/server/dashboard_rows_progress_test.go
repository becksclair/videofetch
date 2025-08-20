package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"videofetch/internal/download"
)

// fakeMgr allows dynamic progress via a captured variable
type fakeMgr struct {
	prog *float64
}

func (f *fakeMgr) Enqueue(url string) (string, error)                            { return "id-1", nil }
func (f *fakeMgr) AttachDB(id string, dbID int64)                                {}
func (f *fakeMgr) SetMeta(id string, title string, duration int64, thumb string) {}
func (f *fakeMgr) Snapshot(id string) []*download.Item {
	p := 0.0
	if f.prog != nil {
		p = *f.prog
	}
	it := &download.Item{ID: "id-1", URL: "https://example.com", Progress: p, State: download.StateDownloading}
	if id != "" {
		it.ID = id
	}
	return []*download.Item{it}
}

func TestDashboardRows_ProgressIncrements(t *testing.T) {
	prog := 1.0
	h := New(&fakeMgr{prog: &prog}, nil)

	// First call: ~1%
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard/rows", nil)
	req1.Header.Set("X-Forwarded-For", "203.0.113.100")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("status=%d", w1.Code)
	}
	body1 := w1.Body.String()
	if !strings.Contains(body1, "width:1%") {
		t.Fatalf("expected width:1%% in rows, got: %q", body1)
	}

	// Second call: mid progress
	prog = 47.0
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard/rows", nil)
	req2.Header.Set("X-Forwarded-For", "203.0.113.100")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status=%d", w2.Code)
	}
	body2 := w2.Body.String()
	if !strings.Contains(body2, "width:47%") { // ensure not jumping to 100
		t.Fatalf("expected width:47%%, got: %q", body2)
	}

	// Final call: complete
	prog = 100
	req3 := httptest.NewRequest(http.MethodGet, "/dashboard/rows", nil)
	req3.Header.Set("X-Forwarded-For", "203.0.113.100")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("status=%d", w3.Code)
	}
	body3 := w3.Body.String()
	if !strings.Contains(body3, "width:100%") {
		t.Fatalf("expected width:100%%, got: %q", body3)
	}
}
