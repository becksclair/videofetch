package download

import "testing"

func TestExtractFilename_PrefersMergedOverDestination(t *testing.T) {
	log := `
[youtube] abc123: Downloading webpage
[download] Destination: /videos/Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.f628.mp4
[download] Destination: /videos/Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.f251.m4a
[Merger] Merging formats into "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
[ffmpeg] Post-process file 100%
`
	got := extractFilename(log)
	want := "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestExtractFilename_AlreadyDownloaded(t *testing.T) {
	log := `[download] Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4 has already been downloaded`
	got := extractFilename(log)
	want := "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestExtractFilename_FallbackDestination(t *testing.T) {
	log := `[download] Destination: /videos/Sample Title-xyz123.mp4`
	got := extractFilename(log)
	want := "Sample Title-xyz123.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
