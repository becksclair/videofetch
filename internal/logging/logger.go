package logging

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	// Logger is the global structured logger instance
	Logger *slog.Logger
)

// Init initializes the global structured logger
func Init(level slog.Level) {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Format time as ISO8601
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.Format(time.RFC3339))
				}
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	Logger = slog.New(handler)
	slog.SetDefault(Logger)
}

// ParseLevel converts a string log level to slog.Level
func ParseLevel(level string) slog.Level {
	switch level {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "info", "INFO":
		return slog.LevelInfo
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Helper functions for common logging patterns

// RedactURL removes secrets from URL logs while retaining debugging value.
// It strips userinfo and masks query parameter values.
func RedactURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return rawURL
	}

	parsed.User = nil

	if parsed.RawQuery != "" {
		query := parsed.Query()
		for key := range query {
			query.Set(key, "***")
		}
		parsed.RawQuery = query.Encode()
	}

	return parsed.String()
}

// LogDownloadStart logs the start of a download
func LogDownloadStart(downloadID, dbID string, url string) {
	if Logger == nil {
		return
	}
	Logger.Info("download started",
		"event", "download_start",
		"download_id", downloadID,
		"db_id", dbID,
		"url", RedactURL(url))
}

// LogDownloadProgress logs download progress updates
func LogDownloadProgress(downloadID, dbID string, progress float64, url string) {
	if Logger == nil {
		return
	}
	Logger.Debug("download progress",
		"event", "download_progress",
		"download_id", downloadID,
		"db_id", dbID,
		"progress", progress,
		"url", RedactURL(url))
}

// LogDownloadComplete logs successful download completion
func LogDownloadComplete(downloadID, dbID, filename string) {
	if Logger == nil {
		return
	}
	Logger.Info("download complete",
		"event", "download_complete",
		"download_id", downloadID,
		"db_id", dbID,
		"filename", filename)
}

// LogDownloadError logs download failures
func LogDownloadError(downloadID, dbID, msg string, err error) {
	if Logger == nil {
		return
	}
	Logger.Error(msg,
		"event", "download_error",
		"download_id", downloadID,
		"db_id", dbID,
		"error", err)
}

// LogDownloadStateChange logs download state transitions
func LogDownloadStateChange(downloadID string, url string, state string) {
	if Logger == nil {
		return
	}
	Logger.Info("download state changed",
		"event", "download_state_change",
		"download_id", downloadID,
		"url", RedactURL(url),
		"state", state)
}

// LogDBOperation logs database operations
func LogDBOperation(operation string, id int64, err error) {
	if Logger == nil {
		return
	}
	if err != nil {
		Logger.Error("database operation failed",
			"event", "db_operation_error",
			"operation", operation,
			"id", id,
			"error", err)
	} else {
		Logger.Info("database operation",
			"event", "db_operation",
			"operation", operation,
			"id", id)
	}
}

// LogDBCreate logs database record creation
func LogDBCreate(id int64, url, title string, duration int, status string, progress float64) {
	if Logger == nil {
		return
	}
	Logger.Info("database record created",
		"event", "db_create",
		"id", id,
		"url", RedactURL(url),
		"title", title,
		"duration", duration,
		"status", status,
		"progress", progress)
}

// LogDBUpdate logs database updates
func LogDBUpdate(operation string, id int64, fields map[string]any) {
	if Logger == nil {
		return
	}
	attrs := []any{
		"event", "db_update",
		"operation", operation,
		"id", id,
	}
	for k, v := range fields {
		if strings.EqualFold(k, "url") {
			if urlValue, ok := v.(string); ok {
				v = RedactURL(urlValue)
			}
		}
		attrs = append(attrs, k, v)
	}
	Logger.Info("database updated", attrs...)
}

// LogHTTPRequest logs HTTP request handling
func LogHTTPRequest(method, path, remoteAddr string, duration time.Duration, status int, responseBytes int) {
	if Logger == nil {
		return
	}
	Logger.Info("http request",
		"event", "http_request",
		"method", method,
		"path", path,
		"remote_addr", remoteAddr,
		"duration_ms", duration.Milliseconds(),
		"status", status,
		"response_bytes", responseBytes)
}

// LogServerStart logs server startup
func LogServerStart(addr string, config map[string]any) {
	if Logger == nil {
		return
	}
	attrs := []any{
		"event", "server_start",
		"addr", addr,
	}
	for k, v := range config {
		attrs = append(attrs, k, v)
	}
	Logger.Info("server started", attrs...)
}

// LogServerShutdown logs server shutdown events
func LogServerShutdown(msg string, err error) {
	if Logger == nil {
		return
	}
	if err != nil {
		Logger.Error(msg,
			"event", "server_shutdown_error",
			"error", err)
	} else {
		Logger.Info(msg,
			"event", "server_shutdown")
	}
}

// LogRetryFailed logs retry of failed downloads
func LogRetryFailed(affected int64, err error) {
	if Logger == nil {
		return
	}
	if err != nil {
		Logger.Error("retry failed downloads error",
			"event", "retry_failed_error",
			"error", err)
	} else {
		Logger.Info("retried failed downloads",
			"event", "retry_failed",
			"affected", affected)
	}
}

// LogMetadataFetch logs metadata fetching operations
func LogMetadataFetch(url string, dbID int64, err error) {
	if Logger == nil {
		return
	}
	if err != nil {
		Logger.Error("metadata fetch failed",
			"event", "metadata_fetch_error",
			"url", RedactURL(url),
			"db_id", dbID,
			"error", err)
	} else {
		Logger.Info("metadata fetched",
			"event", "metadata_fetch",
			"url", RedactURL(url),
			"db_id", dbID)
	}
}

// LogYTDLPCommand logs yt-dlp command execution
func LogYTDLPCommand(id, url, output string, success bool) {
	if Logger == nil {
		return
	}
	if success {
		Logger.Info("yt-dlp command success",
			"event", "ytdlp_success",
			"download_id", id,
			"url", RedactURL(url),
			"output", output)
	} else {
		Logger.Info("yt-dlp command started",
			"event", "ytdlp_start",
			"download_id", id,
			"url", RedactURL(url),
			"output", output)
	}
}

// LogProgressScanError logs progress scanning errors
func LogProgressScanError(id string, err error) {
	if Logger == nil {
		return
	}
	Logger.Warn("progress scan error",
		"event", "progress_scan_error",
		"download_id", id,
		"error", err)
}

// With returns a logger with additional context
func With(ctx context.Context, attrs ...any) *slog.Logger {
	if Logger == nil {
		return slog.Default()
	}
	return Logger.With(attrs...)
}
