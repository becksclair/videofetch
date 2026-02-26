package download

import (
	"os/exec"
	"sync/atomic"
	"testing"
)

func TestExecuteWithProgressTracking_FilenameCallbackOnlyOnSuccess(t *testing.T) {
	d := NewDownloader(t.TempDir())

	var called atomic.Bool
	d.SetFilenameCallback(func(id, filename string) {
		called.Store(true)
	})

	failCmd := exec.Command("sh", "-c", "echo '[download] Destination: sample.mp4' >&2; exit 1")
	if err := d.executeWithProgressTracking("id-fail", failCmd); err == nil {
		t.Fatalf("expected executeWithProgressTracking to fail")
	}
	if called.Load() {
		t.Fatalf("expected filename callback to not run on failed command")
	}

	called.Store(false)
	okCmd := exec.Command("sh", "-c", "echo '[download] Destination: sample.mp4' >&2; exit 0")
	if err := d.executeWithProgressTracking("id-ok", okCmd); err != nil {
		t.Fatalf("expected successful command, got %v", err)
	}
	if !called.Load() {
		t.Fatalf("expected filename callback to run on successful command")
	}
}
