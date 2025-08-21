package download

import "testing"

func TestExtractFilename_PrefersMergedOverDestination(t *testing.T) {
	m := &Manager{}
	log := `
[youtube] abc123: Downloading webpage
[download] Destination: /videos/Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.f628.mp4
[download] Destination: /videos/Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.f251.m4a
[Merger] Merging formats into "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
[ffmpeg] Post-process file 100%
`
	got := m.extractFilename(log)
	want := "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestExtractFilename_AlreadyDownloaded(t *testing.T) {
	m := &Manager{}
	log := `[download] Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4 has already been downloaded`
	got := m.extractFilename(log)
	want := "Stop Killing Processes! Make Ctrl+C Meow Instead… (with Signals)-m6WXrC9Mxzo.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestExtractFilename_FallbackDestination(t *testing.T) {
	m := &Manager{}
	log := `[download] Destination: /videos/Sample Title-xyz123.mp4`
	got := m.extractFilename(log)
	want := "Sample Title-xyz123.mp4"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}
