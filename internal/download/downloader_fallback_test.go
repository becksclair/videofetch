package download

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildYTDLPArgs_EmbedThumbnailToggle(t *testing.T) {
	withThumbnail := buildYTDLPArgs("https://example.com", "%(title)s", "/tmp/out", "/tmp/tmp", true)
	withoutThumbnail := buildYTDLPArgs("https://example.com", "%(title)s", "/tmp/out", "/tmp/tmp", false)

	if !containsArg(withThumbnail, "--embed-thumbnail") {
		t.Fatalf("expected args to include --embed-thumbnail when enabled")
	}
	if containsArg(withoutThumbnail, "--embed-thumbnail") {
		t.Fatalf("expected args to omit --embed-thumbnail when disabled")
	}
}

func TestShouldRetryWithoutThumbnail(t *testing.T) {
	if !shouldRetryWithoutThumbnail(assertErr("yt-dlp: exit status 1: ERROR: Postprocessing: Error opening output files: Invalid argument")) {
		t.Fatalf("expected known thumbnail postprocessing error to trigger fallback")
	}
	if shouldRetryWithoutThumbnail(assertErr("yt-dlp: exit status 1: ERROR: network failure")) {
		t.Fatalf("expected unrelated errors to skip thumbnail fallback")
	}
}

func TestDownload_RetriesWithoutThumbnailOnKnownPostprocessError(t *testing.T) {
	outDir := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(fakeBin) failed: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "yt-dlp-invocations.log")
	fakePath := filepath.Join(fakeBin, "yt-dlp")
	fakeScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]]; then
  echo "supports --progress-template"
  exit 0
fi
if [[ -n "${YTDLP_TEST_LOG:-}" ]]; then
  printf '%s\n' "$*" >> "${YTDLP_TEST_LOG}"
fi
if [[ "$*" == *"--embed-thumbnail"* ]]; then
  echo "ERROR: Postprocessing: Error opening output files: Invalid argument" >&2
  exit 1
fi
echo "[download] Destination: ok.mp4" >&2
exit 0
`
	if err := os.WriteFile(fakePath, []byte(fakeScript), 0o755); err != nil {
		t.Fatalf("WriteFile(fake yt-dlp) failed: %v", err)
	}

	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", fakeBin+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("Setenv(PATH) failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PATH", origPath)
	})
	if err := os.Setenv("YTDLP_TEST_LOG", logPath); err != nil {
		t.Fatalf("Setenv(YTDLP_TEST_LOG) failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("YTDLP_TEST_LOG")
	})

	d := NewDownloader(outDir)
	var filename atomic.Value
	d.SetFilenameCallback(func(id, name string) {
		filename.Store(name)
	})

	if err := d.Download(context.Background(), "test-id", "https://example.com/video"); err != nil {
		t.Fatalf("Download() failed: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(logPath) failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 yt-dlp invocations, got %d (%q)", len(lines), string(raw))
	}
	if !strings.Contains(lines[0], "--embed-thumbnail") {
		t.Fatalf("expected first invocation to include --embed-thumbnail, got %q", lines[0])
	}
	if strings.Contains(lines[1], "--embed-thumbnail") {
		t.Fatalf("expected retry invocation to remove --embed-thumbnail, got %q", lines[1])
	}

	gotFilename, _ := filename.Load().(string)
	if gotFilename != "ok.mp4" {
		t.Fatalf("expected filename callback with ok.mp4, got %q", gotFilename)
	}

	if _, err := os.Stat(filepath.Join(outDir, ".yt-dlp-tmp", "test-id")); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir cleanup, err=%v", err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func assertErr(msg string) error {
	return &testErr{msg: msg}
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}
