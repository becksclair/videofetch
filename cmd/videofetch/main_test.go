package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestComputeOutputDir_DefaultToHomeVideos(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	got, err := computeOutputDir("")
	if err != nil {
		t.Fatalf("computeOutputDir: %v", err)
	}
	want := filepath.Join(home, "Videos", "videofetch")
	if got != want {
		t.Fatalf("default output dir mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestComputeOutputDir_RelativeAndAbsolute(t *testing.T) {
	// Relative path should be resolved to absolute
	rel := "downloads"
	gotRel, err := computeOutputDir(rel)
	if err != nil {
		t.Fatalf("computeOutputDir(rel): %v", err)
	}
	wantRel, _ := filepath.Abs(rel)
	if gotRel != wantRel {
		t.Fatalf("relative path mismatch:\n got: %s\nwant: %s", gotRel, wantRel)
	}

	// Absolute path should be returned as-is (normalized)
	// Construct an absolute path in a portable way
	base := os.TempDir()
	abs := filepath.Join(base, "vf-abs")
	gotAbs, err := computeOutputDir(abs)
	if err != nil {
		t.Fatalf("computeOutputDir(abs): %v", err)
	}
	// On Windows, filepath.Abs may clean case or separators; compare in a normalized form
	normalize := func(p string) string {
		p = filepath.Clean(p)
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return p
	}
	if normalize(gotAbs) != normalize(abs) {
		t.Fatalf("absolute path mismatch:\n got: %s\nwant: %s", gotAbs, abs)
	}
}
