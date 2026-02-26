package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"
)

func withTestLogger(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	prevLogger := Logger
	prevDefault := slog.Default()
	Logger = testLogger
	slog.SetDefault(testLogger)

	return &buf, func() {
		Logger = prevLogger
		slog.SetDefault(prevDefault)
	}
}

func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("expected log line, got empty buffer")
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &out); err != nil {
		t.Fatalf("failed to decode log line: %v\nline=%q", err, lines[len(lines)-1])
	}
	return out
}

func TestRedactURL(t *testing.T) {
	redacted := RedactURL("https://user:pass@example.com/watch?v=123&token=secret")
	parsed, err := url.Parse(redacted)
	if err != nil {
		t.Fatalf("expected parseable redacted URL, got error: %v", err)
	}
	if parsed.User != nil {
		t.Fatalf("expected userinfo stripped, got %v", parsed.User)
	}
	q := parsed.Query()
	if q.Get("v") != "***" || q.Get("token") != "***" {
		t.Fatalf("expected masked query values, got %q", parsed.RawQuery)
	}
	if parsed.Host != "example.com" || parsed.Path != "/watch" {
		t.Fatalf("expected host/path preserved, got host=%q path=%q", parsed.Host, parsed.Path)
	}
}

func TestRedactURL_InvalidReturnsOriginal(t *testing.T) {
	raw := "://not a real url"
	if got := RedactURL(raw); got != raw {
		t.Fatalf("expected invalid URL to be returned unchanged, got %q", got)
	}
}

func TestLogMetadataFetch_RedactsURL(t *testing.T) {
	buf, restore := withTestLogger(t)
	defer restore()

	LogMetadataFetch("https://user:pw@example.com/path?sig=secret", 42, nil)
	entry := decodeLogLine(t, buf)

	loggedURL, _ := entry["url"].(string)
	if strings.Contains(loggedURL, "secret") || strings.Contains(loggedURL, "user:pw") {
		t.Fatalf("expected redacted URL, got %q", loggedURL)
	}
	if !strings.Contains(loggedURL, "sig=%2A%2A%2A") {
		t.Fatalf("expected masked query value, got %q", loggedURL)
	}
}

func TestLogDBUpdate_RedactsURLField(t *testing.T) {
	buf, restore := withTestLogger(t)
	defer restore()

	LogDBUpdate("update_meta", 7, map[string]any{
		"url": "https://example.com/watch?token=abc",
	})

	entry := decodeLogLine(t, buf)
	loggedURL, _ := entry["url"].(string)
	if strings.Contains(loggedURL, "token=abc") {
		t.Fatalf("expected redacted URL in db update log, got %q", loggedURL)
	}
	if !strings.Contains(loggedURL, "token=%2A%2A%2A") {
		t.Fatalf("expected masked token in db update log, got %q", loggedURL)
	}
}

func TestLogHTTPRequest_IncludesStatusAndBytes(t *testing.T) {
	buf, restore := withTestLogger(t)
	defer restore()

	LogHTTPRequest("GET", "/healthz", "127.0.0.1", 12*time.Millisecond, 503, 19)
	entry := decodeLogLine(t, buf)

	if got := int(entry["status"].(float64)); got != 503 {
		t.Fatalf("expected status 503, got %d", got)
	}
	if got := int(entry["response_bytes"].(float64)); got != 19 {
		t.Fatalf("expected response_bytes 19, got %d", got)
	}
}
