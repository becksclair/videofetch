package download

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractArtifactPaths(t *testing.T) {
	outDir := "/tmp/videofetch-out"
	log := `[download] Destination: /tmp/videofetch-out/video.f248.webm
[download] Destination: ./video.f251.m4a
[Merger] Merging formats into "video.mp4"
[download] video.mp4 has already been downloaded`

	paths := extractArtifactPaths(log, outDir)
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d (%v)", len(paths), paths)
	}
	want := map[string]bool{
		filepath.Clean("/tmp/videofetch-out/video.f248.webm"): true,
		filepath.Clean("/tmp/videofetch-out/video.f251.m4a"):  true,
		filepath.Clean("/tmp/videofetch-out/video.mp4"):       true,
	}
	for _, path := range paths {
		if _, ok := want[path]; ok {
			delete(want, path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing expected paths: %v (got %v)", want, paths)
	}
}

func TestCleanupArtifacts_RemovesTrackedFiles(t *testing.T) {
	outDir := t.TempDir()
	d := NewDownloader(outDir)

	partialA := filepath.Join(outDir, "a.part")
	partialB := filepath.Join(outDir, "nested", "b.part")
	if err := os.MkdirAll(filepath.Dir(partialB), 0o755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}
	if err := os.WriteFile(partialA, []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile(a) failed: %v", err)
	}
	if err := os.WriteFile(partialB, []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile(b) failed: %v", err)
	}

	if err := d.CleanupArtifacts("", "", []string{partialA, partialB}); err != nil {
		t.Fatalf("CleanupArtifacts() failed: %v", err)
	}
	if _, err := os.Stat(partialA); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, err=%v", partialA, err)
	}
	if _, err := os.Stat(partialB); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, err=%v", partialB, err)
	}
}
