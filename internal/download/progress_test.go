package download

import (
	"bufio"
	"strings"
	"testing"
)

// Test that only JSON download lines affect the percentage, and
// that unrelated 100% lines (e.g., from [Merger] or other phases) are ignored.
// If JSON reports full bytes downloaded, allow progress to reach 100.
func TestParseProgress_IgnoresNonDownloadPhases(t *testing.T) {
	// Create a registry and add an item
	registry := NewItemRegistry(10)
	registry.Create("x", "u")

	// Create a downloader with a progress callback that updates the registry
	downloader := NewDownloader(t.TempDir())
	downloader.SetProgressCallback(func(id string, progress float64) {
		registry.SetProgress(id, progress)
	})

	// Simulate typical yt-dlp output with noisy lines and JSON progress lines.
	lines := []string{
		"[youtube] abc123: Downloading webpage",
		"some other noise",
		`{"status": "downloading", "downloaded_bytes": 31000, "total_bytes": 100000}`,
		"[Merger] Merging formats into \"file.mp4\" 100%", // should be ignored
		`{"status": "downloading", "downloaded_bytes": 420000, "total_bytes": 1000000}`,
		"[ffmpeg] Post-process file 100%",                                                // should be ignored
		`{"status": "downloading", "downloaded_bytes": 1000000, "total_bytes": 1000000}`, // should set to final 100 here
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n")))
	downloader.parseProgress("x", sc)

	got := registry.Get("x").Progress
	if got != 100.0 {
		t.Fatalf("expected progress 100.0, got %.1f", got)
	}

	// Re-run with only a non-[download] 100% line first to ensure it does not set to 100
	registry2 := NewItemRegistry(10)
	registry2.Create("y", "u")
	downloader2 := NewDownloader(t.TempDir())
	downloader2.SetProgressCallback(func(id string, progress float64) {
		registry2.SetProgress(id, progress)
	})

	lines2 := []string{
		"[Merger] Merging formats into \"file.mp4\" 100%", // should be ignored
		`{"status": "downloading", "downloaded_bytes": 50000, "total_bytes": 1000000}`,
	}
	sc2 := bufio.NewScanner(strings.NewReader(strings.Join(lines2, "\n")))
	downloader2.parseProgress("y", sc2)
	got2 := registry2.Get("y").Progress
	if got2 != 5.0 {
		t.Fatalf("expected progress 5.0, got %.1f", got2)
	}
}

// Ensure carriage-return-delimited progress updates are handled (no newlines).
func TestParseProgress_SplitsOnCarriageReturn(t *testing.T) {
	registry := NewItemRegistry(10)
	registry.Create("z", "u")

	downloader := NewDownloader(t.TempDir())
	downloader.SetProgressCallback(func(id string, progress float64) {
		registry.SetProgress(id, progress)
	})

	// Simulate a stream where JSON lines are CR-delimited (as with non-newline progress)
	stream := `{"status": "downloading", "downloaded_bytes": 100000, "total_bytes": 1000000}` + "\r" +
		`{"status": "downloading", "downloaded_bytes": 250000, "total_bytes": 1000000}` + "\r" +
		`{"status": "downloading", "downloaded_bytes": 500000, "total_bytes": 1000000}` + "\r"
	sc := bufio.NewScanner(strings.NewReader(stream))
	downloader.parseProgress("z", sc)
	got := registry.Get("z").Progress
	if got != 50.0 {
		t.Fatalf("expected 50.0, got %.1f", got)
	}
}

// Test that total_bytes_estimate is used when total_bytes is not available
func TestParseProgress_UsesEstimateWhenNoTotal(t *testing.T) {
	registry := NewItemRegistry(10)
	registry.Create("a", "u")

	downloader := NewDownloader(t.TempDir())
	downloader.SetProgressCallback(func(id string, progress float64) {
		registry.SetProgress(id, progress)
	})

	// Simulate JSON with no total_bytes, only estimate
	lines := []string{
		`{"status": "downloading", "downloaded_bytes": 750000, "total_bytes": 0, "total_bytes_estimate": 1500000}`,
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n")))
	downloader.parseProgress("a", sc)

	got := registry.Get("a").Progress
	if got != 50.0 {
		t.Fatalf("expected progress 50.0, got %.1f", got)
	}
}

// Test that invalid JSON lines are gracefully skipped
func TestParseProgress_SkipsInvalidJSON(t *testing.T) {
	registry := NewItemRegistry(10)
	registry.Create("b", "u")

	downloader := NewDownloader(t.TempDir())
	downloader.SetProgressCallback(func(id string, progress float64) {
		registry.SetProgress(id, progress)
	})

	lines := []string{
		`not valid json`,
		`{"status": "downloading", "downloaded_bytes": 250000, "total_bytes": 500000}`,
		`{broken json`,
		`{"status": "downloading", "downloaded_bytes": 500000, "total_bytes": 500000}`,
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n")))
	downloader.parseProgress("b", sc)

	got := registry.Get("b").Progress
	if got != 100.0 {
		t.Fatalf("expected progress 100.0, got %.1f", got)
	}
}
