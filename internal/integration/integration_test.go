//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
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
	"videofetch/internal/store"
)

func TestEndToEnd_BatchDownload(t *testing.T) {
	requireYTDLP(t)

	urls := parseURLsEnv()
	if len(urls) == 0 {
		urls = []string{"https://www.youtube.com/watch?v=zGDzdps75ns"}
	}

	ts, outDir := newIntegrationServer(t, 2, 8)
	dbIDs := postBatch(t, ts.URL, urls)
	if len(dbIDs) == 0 {
		t.Fatalf("no db_ids returned")
	}

	rows := waitForRows(t, ts.URL, dbIDs, 3*time.Minute)
	for _, row := range rows {
		if row.Status == "error" && isProviderRestriction(row.ErrorMessage) {
			t.Skipf("skipping due to provider restrictions: %s", row.ErrorMessage)
		}
		if row.Status != "completed" {
			t.Fatalf("download %d ended with status=%s error=%s", row.ID, row.Status, row.ErrorMessage)
		}
	}

	if countFiles(t, outDir) < len(dbIDs) {
		t.Fatalf("expected at least %d output files", len(dbIDs))
	}
}

func TestEndToEnd_DownloadSingle(t *testing.T) {
	requireYTDLP(t)

	url := os.Getenv("INTEGRATION_URL")
	if url == "" {
		url = "https://www.youtube.com/watch?v=zGDzdps75ns"
	}

	ts, outDir := newIntegrationServer(t, 2, 8)
	dbID := postSingle(t, ts.URL, url)

	row := waitForRows(t, ts.URL, []int64{dbID}, 2*time.Minute)[dbID]
	if row.Status == "error" && isProviderRestriction(row.ErrorMessage) {
		t.Skipf("skipping due to provider restrictions: %s", row.ErrorMessage)
	}
	if row.Status != "completed" {
		t.Fatalf("download %d ended with status=%s error=%s", row.ID, row.Status, row.ErrorMessage)
	}

	if countFiles(t, outDir) == 0 {
		t.Fatalf("no files created")
	}
}

func TestProgress_RemainsBoundedAndMonotonic(t *testing.T) {
	requireYTDLP(t)

	url := os.Getenv("INTEGRATION_URL")
	if url == "" {
		url = "https://www.youtube.com/watch?v=zGDzdps75ns"
	}

	ts, _ := newIntegrationServer(t, 1, 4)
	dbID := postSingle(t, ts.URL, url)

	lastProgress := -1.0
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		row, ok := getDownloadByID(t, ts.URL, dbID)
		if !ok {
			continue
		}
		if row.Status == "error" {
			if isProviderRestriction(row.ErrorMessage) {
				t.Skipf("skipping due to provider restrictions: %s", row.ErrorMessage)
			}
			t.Fatalf("failed: %s", row.ErrorMessage)
		}
		if row.Progress < 0 || row.Progress > 100 {
			t.Fatalf("progress out of range: %.1f", row.Progress)
		}
		if row.Progress < lastProgress {
			t.Fatalf("progress decreased from %.1f to %.1f", lastProgress, row.Progress)
		}
		lastProgress = row.Progress
		// yt-dlp can report all download bytes before post-processing and final
		// status persistence complete, so the stable contract is monotonic
		// bounded progress rather than "100 only when completed".
		if row.Status != "completed" {
			continue
		}
		return
	}
	t.Fatalf("timeout waiting for completion")
}

func newIntegrationServer(t *testing.T, workers, queue int) (*httptest.Server, string) {
	t.Helper()

	root := t.TempDir()
	outDir := filepath.Join(root, "downloads")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create output dir: %v", err)
	}

	st, err := store.Open(filepath.Join(root, "videofetch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := download.NewManager(outDir, workers, queue)
	mgr.SetStore(st)

	dbWorker := download.NewDBWorker(st, mgr)
	dbWorker.Start()

	ts := httptest.NewServer(server.New(mgr, st, outDir))
	t.Cleanup(func() {
		ts.Close()
		dbWorker.Stop()
		mgr.Shutdown()
		_ = st.Close()
	})

	return ts, outDir
}

func requireYTDLP(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp not found in PATH; skipping integration test")
	}
}

func postSingle(t *testing.T, baseURL, url string) int64 {
	t.Helper()

	body := map[string]string{"url": url}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/download_single", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post single: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("single status=%d", resp.StatusCode)
	}

	var enq struct {
		DBID int64 `json:"db_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enq); err != nil {
		t.Fatalf("decode single response: %v", err)
	}
	if enq.DBID <= 0 {
		t.Fatalf("empty db_id")
	}
	return enq.DBID
}

func postBatch(t *testing.T, baseURL string, urls []string) []int64 {
	t.Helper()

	body := map[string]any{"urls": urls}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/download", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post batch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d", resp.StatusCode)
	}

	var enq struct {
		DBIDs []int64 `json:"db_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enq); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	return enq.DBIDs
}

func waitForRows(t *testing.T, baseURL string, ids []int64, timeout time.Duration) map[int64]store.Download {
	t.Helper()

	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		rows, err := listDownloads(baseURL)
		if err != nil {
			continue
		}

		found := make(map[int64]store.Download, len(ids))
		done := true
		for _, row := range rows {
			if _, ok := want[row.ID]; !ok {
				continue
			}
			found[row.ID] = row
			if row.Status != "completed" && row.Status != "error" {
				done = false
			}
		}
		if len(found) != len(ids) {
			done = false
		}
		if done {
			return found
		}
	}

	t.Fatalf("timeout waiting for rows: %v", ids)
	return nil
}

func getDownloadByID(t *testing.T, baseURL string, id int64) (store.Download, bool) {
	t.Helper()

	rows, err := listDownloads(baseURL)
	if err != nil {
		t.Fatalf("list downloads: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return store.Download{}, false
}

func listDownloads(baseURL string) ([]store.Download, error) {
	resp, err := http.Get(baseURL + "/api/downloads")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Downloads []store.Download `json:"downloads"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Downloads, nil
}

func countFiles(t *testing.T, root string) int {
	t.Helper()

	count := 0
	if err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk output dir: %v", err)
	}
	return count
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

func isProviderRestriction(errMsg string) bool {
	return strings.Contains(errMsg, "Requested format is not available") ||
		strings.Contains(errMsg, "HTTP Error 403")
}
