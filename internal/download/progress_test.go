package download

import (
	"bufio"
	"strings"
	"testing"
)

// Test that only VFPROG (download) lines affect the percentage, and
// that unrelated 100% lines (e.g., from [Merger] or other phases) are ignored.
// Also ensure the parser does not set progress to 100% prematurely; final 100%
// is set by the worker upon completion.
func TestParseProgress_IgnoresNonDownloadPhases(t *testing.T) {
	m := &Manager{downloads: map[string]*Item{"x": {ID: "x", URL: "u", Progress: 0, State: StateDownloading}}}

	// Simulate typical yt-dlp output with noisy lines.
	lines := []string{
		"[youtube] abc123: Downloading webpage",
		"some other noise",
		"VFPROG {\"p\":\"   3.1%\",\"st\":\"downloading\"}",
		"[Merger] Merging formats into \"file.mp4\" 100%", // should be ignored
		"VFPROG {\"p\":\"  42.0%\",\"st\":\"downloading\"}",
		"[ffmpeg] Post-process file 100%", // should be ignored
		"VFPROG {\"p\":\"100%\",\"st\":\"finished\"}",
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
		"VFPROG {\"p\":\"   5.0%\",\"st\":\"downloading\"}",
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
	// Simulate a stream where VFPROG lines are CR-delimited (as with non-newline progress)
	stream := "VFPROG {\"p\":\"  10.0%\",\"st\":\"downloading\"}\rVFPROG {\"p\":\"  25.0%\",\"st\":\"downloading\"}\rVFPROG {\"p\":\"  50.0%\",\"st\":\"downloading\"}\r"
	sc := bufio.NewScanner(strings.NewReader(stream))
	m.parseProgress("z", sc)
	got := m.Snapshot("z")[0].Progress
	if got != 50.0 {
		t.Fatalf("expected 50.0, got %.1f", got)
	}
}
