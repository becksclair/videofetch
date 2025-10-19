//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/server"
)

// Batch download end-to-end test using /api/download
func TestEndToEnd_BatchDownload(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not found in PATH; skipping integration test")
	}

	urls := parseURLsEnv()
	if len(urls) < 2 {
		// Use the specified YouTube URL for integration tests
		urls = []string{
			"https://www.youtube.com/watch?v=zGDzdps75ns",
			"https://www.youtube.com/watch?v=zGDzdps75ns",
		}
	}

	outDir := t.TempDir()
	mgr := download.NewManager(outDir, 2, 8)
	t.Cleanup(func() { mgr.Shutdown() })

	h := server.New(mgr, nil, tmpDir)
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Enqueue batch download
	reqBody := map[string]any{"urls": urls}
	b, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/download", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	var enq struct {
		Status, Message string
		IDs             []string `json:"ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enq); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(enq.IDs) == 0 {
		t.Fatalf("no ids returned")
	}

	// Poll until all of the returned IDs are completed
	deadline := time.Now().Add(3 * time.Minute)
	completed := make(map[string]bool)
	failed := make(map[string]error)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for completion; completed=%d/%d failed=%d", countTrue(completed), len(enq.IDs), len(failed))
		}
		time.Sleep(3 * time.Second)
		// Fetch snapshot of all downloads once per loop
		r, err := http.Get(ts.URL + "/api/status")
		if err != nil {
			continue
		}
		var st struct {
			Downloads []struct {
				ID    string `json:"id"`
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"downloads"`
		}
		func() { defer r.Body.Close(); _ = json.NewDecoder(r.Body).Decode(&st) }()
		// classify states for our IDs only
		for _, d := range st.Downloads {
			if !contains(enq.IDs, d.ID) {
				continue
			}
			switch d.State {
			case string(download.StateCompleted):
				completed[d.ID] = true
			case string(download.StateFailed):
				failed[d.ID] = errors.New(d.Error)
			}
		}
		if len(completed) == len(enq.IDs) {
			break
		}
		if len(failed) > 0 {
			t.Fatalf("some downloads failed: %v", failed)
		}
	}

	// Verify at least as many files as IDs exist
	var files []string
	_ = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if len(files) < len(enq.IDs) {
		t.Fatalf("expected >= %d files, found %d", len(enq.IDs), len(files))
	}
}

// Single download end-to-end as a smoke test
func TestEndToEnd_DownloadSingle(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not found in PATH; skipping integration test")
	}
	url := os.Getenv("INTEGRATION_URL")
	if url == "" {
		// Use the specified YouTube URL for integration tests
		url = "https://www.youtube.com/watch?v=zGDzdps75ns"
	}
	outDir := t.TempDir()
	mgr := download.NewManager(outDir, 2, 8)
	t.Cleanup(func() { mgr.Shutdown() })
	h := server.New(mgr, nil, tmpDir)
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := map[string]string{"url": url}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/download_single", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var enq struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&enq)
	if enq.ID == "" {
		t.Fatalf("empty id")
	}

	deadline := time.Now().Add(2 * time.Minute)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for completion")
		}
		time.Sleep(2 * time.Second)
		r, err := http.Get(ts.URL + "/api/status?id=" + enq.ID)
		if err != nil {
			continue
		}
		var last struct {
			Downloads []struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"downloads"`
		}
		func() { defer r.Body.Close(); _ = json.NewDecoder(r.Body).Decode(&last) }()
		if len(last.Downloads) == 1 {
			st := last.Downloads[0].State
			if st == string(download.StateCompleted) {
				break
			}
			if st == string(download.StateFailed) {
				// Skip for common provider/region flakiness rather than fail the suite
				if strings.Contains(last.Downloads[0].Error, "Requested format is not available") ||
					strings.Contains(last.Downloads[0].Error, "HTTP Error 403") {
					t.Skipf("skipping due to provider restrictions: %s", last.Downloads[0].Error)
				}
				t.Fatalf("failed: %s", last.Downloads[0].Error)
			}
		}
	}
	// Verify file created
	var count int
	_ = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			count++
		}
		return nil
	})
	if count == 0 {
		t.Fatalf("no files created")
	}
}

func parseURLsEnv() []string {
	if s := os.Getenv("INTEGRATION_URLS"); s != "" {
		fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
		out := make([]string, 0, len(fields))
		for _, f := range fields {
			if f != "" {
				out = append(out, f)
			}
		}
		return out
	}
	if s := os.Getenv("INTEGRATION_URL"); s != "" {
		return []string{s}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func countTrue(m map[string]bool) int {
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return n
}

// Verify progress does not jump to 100% at start and that we observe
// an in-flight progress value (0 < p < 100) before completion.
func TestProgress_NoEarlyHundred(t *testing.T) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not found in PATH; skipping integration test")
	}

	url := os.Getenv("INTEGRATION_URL")
	if url == "" {
		// Use the specified YouTube URL for integration tests
		url = "https://www.youtube.com/watch?v=zGDzdps75ns"
	}


	outDir := t.TempDir()
	mgr := download.NewManager(outDir, 1, 4)
	t.Cleanup(func() { mgr.Shutdown() })
	h := server.New(mgr, nil, tmpDir)
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Enqueue single download via API
	body := map[string]string{"url": url}
	bb, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/download_single", bytes.NewReader(bb))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var enq struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&enq)
	if enq.ID == "" {
		t.Fatalf("empty id")
	}

	deadline := time.Now().Add(2 * time.Minute)
	sawMid := false
	completed := false
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		r, err := http.Get(ts.URL + "/api/status?id=" + enq.ID)
		if err != nil {
			continue
		}
		var last struct {
			Downloads []struct {
				State    string  `json:"state"`
				Error    string  `json:"error"`
				Progress float64 `json:"progress"`
			} `json:"downloads"`
		}
		func() { defer r.Body.Close(); _ = json.NewDecoder(r.Body).Decode(&last) }()
		if len(last.Downloads) != 1 {
			continue
		}
		st := last.Downloads[0].State
		p := last.Downloads[0].Progress
		if st == string(download.StateFailed) {
			if strings.Contains(last.Downloads[0].Error, "Requested format is not available") ||
				strings.Contains(last.Downloads[0].Error, "HTTP Error 403") {
				t.Skipf("skipping due to provider restrictions: %s", last.Downloads[0].Error)
			}
			t.Fatalf("failed: %s", last.Downloads[0].Error)
		}
		if st != string(download.StateCompleted) {
			if p >= 100 {
				t.Fatalf("progress reached 100%% before completion; state=%s", st)
			}
			if p > 0 && p < 100 {
				sawMid = true
			}
		} else {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatalf("timeout waiting for completion")
	}
	if !sawMid {
		t.Fatalf("did not observe mid-progress (0<p<100) before completion")
	}
}
