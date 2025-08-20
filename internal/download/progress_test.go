package download

import (
	"bufio"
	"strings"
	"testing"
)

// Test that only remedia (download) lines affect the percentage, and
// that unrelated 100% lines (e.g., from [Merger] or other phases) are ignored.
// Also ensure the parser does not set progress to 100% prematurely; final 100%
// is set by the worker upon completion.
func TestParseProgress_IgnoresNonDownloadPhases(t *testing.T) {
	m := &Manager{downloads: map[string]*Item{"x": {ID: "x", URL: "u", Progress: 0, State: StateDownloading}}}

	// Simulate typical yt-dlp output with noisy lines and remedia progress lines.
	lines := []string{
		"[youtube] abc123: Downloading webpage",
		"some other noise",
		"remedia-31000-100000-0-10",
		"[Merger] Merging formats into \"file.mp4\" 100%", // should be ignored
		"remedia-420000-1000000-0-5",
		"[ffmpeg] Post-process file 100%", // should be ignored
		"remedia-1000000-1000000-0-0",     // should not set to final 100 here
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n")))
	m.parseProgress("x", sc)

	got := m.Snapshot("x")[0].Progress
	if !(got >= 99 && got < 100) {
		t.Fatalf("expected progress close to 100 but <100, got %.1f", got)
	}

	// Re-run with only a non-[download] 100% line first to ensure it does not set to 100
	m2 := &Manager{downloads: map[string]*Item{"y": {ID: "y", URL: "u", Progress: 0, State: StateDownloading}}}
	lines2 := []string{
		"[Merger] Merging formats into \"file.mp4\" 100%", // should be ignored
		"remedia-50000-1000000-0-0",
	}
	sc2 := bufio.NewScanner(strings.NewReader(strings.Join(lines2, "\n")))
	m2.parseProgress("y", sc2)
	got2 := m2.Snapshot("y")[0].Progress
	if got2 != 5.0 {
		t.Fatalf("expected progress 5.0, got %.1f", got2)
	}
}

// Ensure carriage-return-delimited progress updates are handled (no newlines).
func TestParseProgress_SplitsOnCarriageReturn(t *testing.T) {
	m := &Manager{downloads: map[string]*Item{"z": {ID: "z", URL: "u", Progress: 0, State: StateDownloading}}}
	// Simulate a stream where remedia lines are CR-delimited (as with non-newline progress)
	stream := "remedia-100000-1000000-0-0\rremedia-250000-1000000-0-0\rremedia-500000-1000000-0-0\r"
	sc := bufio.NewScanner(strings.NewReader(stream))
	m.parseProgress("z", sc)
	got := m.Snapshot("z")[0].Progress
	if got != 50.0 {
		t.Fatalf("expected 50.0, got %.1f", got)
	}
}
