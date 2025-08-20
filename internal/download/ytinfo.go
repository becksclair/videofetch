package download

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// MediaInfo contains minimal metadata extracted from yt-dlp -j.
type MediaInfo struct {
	Title        string
	DurationSec  int64
	ThumbnailURL string
}

// FetchMediaInfo runs `yt-dlp -j` and returns the first parsed media info.
// On failure, returns a zero MediaInfo and an error.
func FetchMediaInfo(inputURL string) (MediaInfo, error) {
	if err := CheckYTDLP(); err != nil {
		return MediaInfo{}, err
	}
	// Validate URL to prevent command injection
	if err := validateURL(inputURL); err != nil {
		return MediaInfo{}, fmt.Errorf("invalid URL: %w", err)
	}
	// Mirror the Rust example: use -j and pass extractor args to impersonate
	// the generic extractor when probing metadata to improve robustness.
	cmd := exec.Command("yt-dlp", "-j", "--extractor-args", "generic:impersonate", "--no-playlist", inputURL)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return MediaInfo{}, err
	}
	if err := cmd.Start(); err != nil {
		return MediaInfo{}, err
	}
	defer cmd.Wait()
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" {
			continue
		}
		// Parse generically to allow missing fields.
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			continue
		}
		var title string
		if v, ok := m["title"].(string); ok && v != "" {
			title = v
		} else {
			title = inputURL
		}
		var duration int64
		switch dv := m["duration"].(type) {
		case float64:
			duration = int64(dv)
		case int64:
			duration = dv
		}
		var thumb string
		if v, ok := m["thumbnail"].(string); ok {
			thumb = v
		}
		if thumb == "" {
			if arr, ok := m["thumbnails"].([]any); ok && len(arr) > 0 {
				if obj, ok := arr[0].(map[string]any); ok {
					if u, ok := obj["url"].(string); ok {
						thumb = u
					}
				}
			}
		}
		return MediaInfo{Title: title, DurationSec: duration, ThumbnailURL: thumb}, nil
	}
	if err := sc.Err(); err != nil {
		return MediaInfo{}, err
	}
	return MediaInfo{}, errors.New("no_media_info")
}

// validateURL ensures the URL is safe to pass to external commands
func validateURL(rawURL string) error {
	if rawURL == "" {
		return errors.New("empty URL")
	}
	if len(rawURL) > 2048 {
		return errors.New("URL too long")
	}
	// Parse URL to validate structure
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}
	// Only allow http/https schemes
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}
	// Ensure host is present
	if parsed.Host == "" {
		return errors.New("missing host")
	}
	// Check for shell metacharacters that could be dangerous (but allow & in query params)
	dangerous := []string{";", "|", "`", "$", "(", ")", "<", ">"}
	for _, char := range dangerous {
		if strings.Contains(rawURL, char) {
			return fmt.Errorf("URL contains dangerous character: %s", char)
		}
	}
	// Check for newlines/carriage returns separately since they're handled by URL parsing
	if strings.ContainsAny(rawURL, "\n\r") {
		return errors.New("URL contains line breaks")
	}
	return nil
}
