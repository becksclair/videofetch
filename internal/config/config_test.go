package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	cfg := New()

	if cfg.Host != "0.0.0.0" {
		t.Errorf("expected default Host = 0.0.0.0, got %s", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected default Port = 8080, got %d", cfg.Port)
	}
	if cfg.Workers != 4 {
		t.Errorf("expected default Workers = 4, got %d", cfg.Workers)
	}
	if cfg.QueueCap != 128 {
		t.Errorf("expected default QueueCap = 128, got %d", cfg.QueueCap)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected default LogLevel = info, got %s", cfg.LogLevel)
	}
	if cfg.UnsafeLogPayloads {
		t.Errorf("expected default UnsafeLogPayloads = false, got true")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: &Config{
				Host:     "localhost",
				Port:     3000,
				Workers:  2,
				QueueCap: 64,
				LogLevel: "debug",
			},
			wantErr: false,
		},
		{
			name: "invalid port low",
			cfg: &Config{
				Port:     0,
				LogLevel: "info",
			},
			wantErr: true,
			errMsg:  "invalid port",
		},
		{
			name: "invalid port high",
			cfg: &Config{
				Port:     70000,
				LogLevel: "info",
			},
			wantErr: true,
			errMsg:  "invalid port",
		},
		{
			name: "invalid log level",
			cfg: &Config{
				Port:     8080,
				LogLevel: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid log level",
		},
		{
			name: "auto-fix workers",
			cfg: &Config{
				Port:     8080,
				Workers:  0,
				LogLevel: "info",
			},
			wantErr: false,
		},
		{
			name: "auto-fix queue",
			cfg: &Config{
				Port:     8080,
				QueueCap: 0,
				LogLevel: "info",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error = %v", err)
				}
				// Check that address was computed
				if tt.cfg.Addr == "" {
					t.Errorf("Validate() should compute Addr")
				}
				// Check auto-fixes
				if tt.cfg.Workers < 1 {
					t.Errorf("Validate() should auto-fix Workers to at least 1, got %d", tt.cfg.Workers)
				}
				if tt.cfg.QueueCap < 1 {
					t.Errorf("Validate() should auto-fix QueueCap to at least 1, got %d", tt.cfg.QueueCap)
				}
			}
		})
	}
}

func TestResolveOutputDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		name     string
		input    string
		wantPath string // expected substring in resolved path
	}{
		{
			name:     "empty defaults to home/Videos/videofetch",
			input:    "",
			wantPath: filepath.Join(home, "Videos", "videofetch"),
		},
		{
			name:     "tilde expansion",
			input:    "~/Downloads",
			wantPath: filepath.Join(home, "Downloads"),
		},
		{
			name:     "absolute path unchanged",
			input:    "/tmp/videos",
			wantPath: "/tmp/videos",
		},
		{
			name:     "relative path resolved",
			input:    "downloads",
			wantPath: "downloads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{OutputDir: tt.input}
			err := cfg.ResolveOutputDir()
			if err != nil {
				t.Errorf("ResolveOutputDir() error = %v", err)
				return
			}
			if cfg.AbsOutputDir == "" {
				t.Errorf("ResolveOutputDir() didn't set AbsOutputDir")
			}
			if !strings.Contains(cfg.AbsOutputDir, tt.wantPath) {
				t.Errorf("ResolveOutputDir() = %v, want to contain %v", cfg.AbsOutputDir, tt.wantPath)
			}
			// Should be an absolute path
			if !filepath.IsAbs(cfg.AbsOutputDir) {
				t.Errorf("ResolveOutputDir() = %v, want absolute path", cfg.AbsOutputDir)
			}
		})
	}
}

func TestResolveDBPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		name     string
		input    string
		wantPath string // expected substring
	}{
		{
			name:  "empty defaults to cache",
			input: "",
			wantPath: func() string {
				if runtime.GOOS == "windows" {
					return filepath.Join("videofetch", "videofetch.db")
				}
				return filepath.Join(".cache", "videofetch", "videofetch.db")
			}(),
		},
		{
			name:     "tilde expansion",
			input:    "~/.local/share/videofetch.db",
			wantPath: filepath.Join(home, ".local", "share", "videofetch.db"),
		},
		{
			name:     "absolute path unchanged",
			input:    "/var/lib/videofetch.db",
			wantPath: "/var/lib/videofetch.db",
		},
		{
			name:     "relative path resolved",
			input:    "data/videofetch.db",
			wantPath: "videofetch.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{DBPath: tt.input}
			err := cfg.ResolveDBPath()
			if err != nil {
				t.Errorf("ResolveDBPath() error = %v", err)
				return
			}
			if cfg.AbsDBPath == "" {
				t.Errorf("ResolveDBPath() didn't set AbsDBPath")
			}
			if !strings.Contains(cfg.AbsDBPath, tt.wantPath) {
				t.Errorf("ResolveDBPath() = %v, want to contain %v", cfg.AbsDBPath, tt.wantPath)
			}
			// Should be an absolute path
			if !filepath.IsAbs(cfg.AbsDBPath) {
				t.Errorf("ResolveDBPath() = %v, want absolute path", cfg.AbsDBPath)
			}
		})
	}
}

func TestComputeAddr(t *testing.T) {
	tests := []struct {
		host string
		port int
		want string
	}{
		{"localhost", 8080, "localhost:8080"},
		{"0.0.0.0", 3000, "0.0.0.0:3000"},
		{"127.0.0.1", 9999, "127.0.0.1:9999"},
		{"", 8080, ":8080"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			cfg := &Config{Host: tt.host, Port: tt.port}
			got := cfg.ComputeAddr()
			if got != tt.want {
				t.Errorf("ComputeAddr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	cfg := &Config{
		Host:              "localhost",
		Port:              8080,
		Addr:              "localhost:8080",
		OutputDir:         "~/Videos",
		AbsOutputDir:      "/home/user/Videos",
		DBPath:            "videofetch.db",
		AbsDBPath:         "/home/user/.cache/videofetch/videofetch.db",
		Workers:           4,
		QueueCap:          128,
		LogLevel:          "info",
		UnsafeLogPayloads: true,
		Version:           "1.0.0",
	}

	str := cfg.String()
	if !strings.Contains(str, "localhost:8080") {
		t.Errorf("String() missing addr")
	}
	if !strings.Contains(str, "Workers: 4") {
		t.Errorf("String() missing workers")
	}
	if !strings.Contains(str, "LogLevel: info") {
		t.Errorf("String() missing log level")
	}
	if !strings.Contains(str, "UnsafeLogPayloads: true") {
		t.Errorf("String() missing unsafe payload logging flag")
	}
}

func TestSummary(t *testing.T) {
	cfg := &Config{
		Addr:              "localhost:8080",
		AbsOutputDir:      "/home/user/Videos",
		AbsDBPath:         "/home/user/.cache/videofetch.db",
		Workers:           4,
		QueueCap:          128,
		LogLevel:          "info",
		UnsafeLogPayloads: true,
		Version:           "1.0.0",
	}

	summary := cfg.Summary()

	if summary["addr"] != "localhost:8080" {
		t.Errorf("Summary() addr = %v, want localhost:8080", summary["addr"])
	}
	if summary["workers"] != 4 {
		t.Errorf("Summary() workers = %v, want 4", summary["workers"])
	}
	if summary["log_level"] != "info" {
		t.Errorf("Summary() log_level = %v, want info", summary["log_level"])
	}
	if summary["unsafe_log_payloads"] != true {
		t.Errorf("Summary() unsafe_log_payloads = %v, want true", summary["unsafe_log_payloads"])
	}
}
