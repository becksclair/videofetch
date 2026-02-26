package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config holds all configuration for the videofetch application
type Config struct {
	// Server configuration
	Host string
	Port int
	Addr string // computed from Host:Port

	// File system
	OutputDir    string // user-provided
	AbsOutputDir string // resolved/absolute path
	DBPath       string // user-provided
	AbsDBPath    string // resolved/absolute path

	// Download behavior
	Workers  int // concurrent workers
	QueueCap int // max pending jobs

	// Logging
	LogLevel          string // debug|info|warn|error
	UnsafeLogPayloads bool

	// Validation & computed
	Version   string    // app version
	StartTime time.Time // when the app started
}

// New creates a Config with default values
func New() *Config {
	return &Config{
		Host:      "0.0.0.0",
		Port:      8080,
		Workers:   4,
		QueueCap:  128,
		LogLevel:  "info",
		StartTime: time.Now(),
		Version:   "1.0.0", // TODO: could be set from build flags
	}
}

// Validate checks that all required configuration is present and valid
func (c *Config) Validate() error {
	// Validate port range
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", c.Port)
	}

	// Validate workers
	if c.Workers < 1 {
		c.Workers = runtime.NumCPU()
		if c.Workers < 1 {
			c.Workers = 1
		}
	}

	// Validate queue capacity
	if c.QueueCap < 1 {
		c.QueueCap = 128
	}

	// Validate log level
	validLevels := []string{"debug", "info", "warn", "error"}
	c.LogLevel = strings.ToLower(c.LogLevel)
	valid := false
	for _, level := range validLevels {
		if c.LogLevel == level {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid log level: %s (must be debug|info|warn|error)", c.LogLevel)
	}

	// Compute address
	c.Addr = c.ComputeAddr()

	return nil
}

// ResolveOutputDir expands the output directory path and resolves it to an absolute path
// If empty, defaults to $HOME/Videos/videofetch
func (c *Config) ResolveOutputDir() error {
	if c.OutputDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		c.OutputDir = filepath.Join(home, "Videos", "videofetch")
	}

	// Expand ~ if present
	if strings.HasPrefix(c.OutputDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expand home directory: %w", err)
		}
		c.OutputDir = filepath.Join(home, c.OutputDir[2:]) // Skip "~/"
	} else if c.OutputDir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expand home directory: %w", err)
		}
		c.OutputDir = home
	}

	// Resolve to absolute path
	abs, err := filepath.Abs(c.OutputDir)
	if err != nil {
		return fmt.Errorf("resolve absolute path for %s: %w", c.OutputDir, err)
	}
	c.AbsOutputDir = abs

	return nil
}

// ResolveDBPath expands the database path and resolves it to an absolute path
// If empty, defaults to OS cache directory
func (c *Config) ResolveDBPath() error {
	if c.DBPath == "" {
		c.DBPath = defaultCacheDBPath()
	}

	// Expand ~ if present
	if strings.HasPrefix(c.DBPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expand home directory: %w", err)
		}
		c.DBPath = filepath.Join(home, c.DBPath[2:]) // Skip "~/"
	} else if c.DBPath == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("expand home directory: %w", err)
		}
		c.DBPath = home
	}

	// Resolve to absolute path
	abs, err := filepath.Abs(c.DBPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path for %s: %w", c.DBPath, err)
	}
	c.AbsDBPath = abs

	return nil
}

// ComputeAddr returns the full server address as host:port
func (c *Config) ComputeAddr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// String returns a pretty-printed representation of the configuration
func (c *Config) String() string {
	return fmt.Sprintf(`Config{
  Server:
    Host: %s
    Port: %d
    Addr: %s
  Files:
    OutputDir: %s (resolved: %s)
    DBPath: %s (resolved: %s)
  Download:
    Workers: %d
    QueueCap: %d
  Logging:
    LogLevel: %s
    UnsafeLogPayloads: %t
  Meta:
    Version: %s
    StartTime: %s
}`, c.Host, c.Port, c.Addr,
		c.OutputDir, c.AbsOutputDir,
		c.DBPath, c.AbsDBPath,
		c.Workers, c.QueueCap,
		c.LogLevel, c.UnsafeLogPayloads,
		c.Version, c.StartTime.Format(time.RFC3339))
}

// Summary returns a one-line summary of key configuration
func (c *Config) Summary() map[string]any {
	return map[string]any{
		"addr":                c.Addr,
		"output_dir":          c.AbsOutputDir,
		"db_path":             c.AbsDBPath,
		"workers":             c.Workers,
		"queue":               c.QueueCap,
		"log_level":           c.LogLevel,
		"unsafe_log_payloads": c.UnsafeLogPayloads,
		"version":             c.Version,
	}
}

// defaultCacheDBPath returns the cross-platform default path for the SQLite DB
// - Windows: %APPDATA%/videofetch/videofetch.db
// - Linux/macOS: $HOME/.cache/videofetch/videofetch.db
func defaultCacheDBPath() string {
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "videofetch", "videofetch.db")
		}
		// Fallback to user home if APPDATA is not set
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Roaming", "videofetch", "videofetch.db")
		}
		// Last resort: current directory
		return "videofetch.db"
	}
	// Linux/macOS default cache location
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "videofetch", "videofetch.db")
	}
	// Fallback: place in working directory
	return filepath.Join("videofetch", "videofetch.db")
}
