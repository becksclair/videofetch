package download

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
)

const maxMediaInfoBytes = 64 * 1024 * 1024 // 64 MiB

type ytInfoJSON struct {
	Title      string  `json:"title"`
	Duration   float64 `json:"duration"`
	Thumbnail  string  `json:"thumbnail"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
	// Set when the input URL expanded to a playlist; decoded loosely so type drift never breaks parsing.
	PlaylistID    any `json:"playlist_id"`
	PlaylistIndex any `json:"playlist_index"`
}

// MediaInfo contains minimal metadata extracted from yt-dlp -j.
type MediaInfo struct {
	Title        string
	DurationSec  int64
	ThumbnailURL string
	RawInfoJSON  []byte
}

// FetchMediaInfo runs `yt-dlp -j` and returns the first parsed media info.
// On failure, returns a zero MediaInfo and an error.
func FetchMediaInfo(ctx context.Context, inputURL string) (MediaInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return MediaInfo{}, err
	}
	if err := CheckYTDLP(); err != nil {
		return MediaInfo{}, err
	}
	// Validate URL to prevent command injection
	if err := validateURL(inputURL); err != nil {
		return MediaInfo{}, fmt.Errorf("invalid URL: %w", err)
	}
	// Mirror the Rust example: use -j and pass extractor args to impersonate
	// the generic extractor when probing metadata to improve robustness.
	cmd := exec.CommandContext(ctx, "yt-dlp", "-j", "--extractor-args", "generic:impersonate", "--no-playlist", inputURL)
	configureSubprocess(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return MediaInfo{}, err
	}
	if err := cmd.Start(); err != nil {
		return MediaInfo{}, err
	}
	info, parseErr := parseMediaInfoStream(out, inputURL)
	if parseErr != nil {
		_ = killSubprocessGroup(cmd)
	} else {
		_, _ = io.Copy(io.Discard, out)
	}
	_ = cmd.Wait()
	if parseErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return MediaInfo{}, ctxErr
		}
		return MediaInfo{}, parseErr
	}
	return info, nil
}

func parseMediaInfoStream(r io.Reader, inputURL string) (MediaInfo, error) {
	limited := io.LimitReader(r, maxMediaInfoBytes+1)
	br := bufio.NewReaderSize(limited, 64*1024)
	var totalRead int64
	for {
		line, err := br.ReadBytes('\n')
		totalRead += int64(len(line))
		if totalRead > maxMediaInfoBytes {
			return MediaInfo{}, fmt.Errorf("metadata response exceeded %d bytes", maxMediaInfoBytes)
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var parsed ytInfoJSON
			if jsonErr := json.Unmarshal(trimmed, &parsed); jsonErr == nil {
				title := parsed.Title
				if title == "" {
					title = inputURL
				}
				thumb := parsed.Thumbnail
				if thumb == "" {
					for _, item := range parsed.Thumbnails {
						if item.URL == "" {
							continue
						}
						if strings.Contains(item.URL, "maxresdefault") || strings.Contains(item.URL, "hqdefault") {
							thumb = item.URL
							break
						}
						if thumb == "" {
							thumb = item.URL
						}
					}
				}
				info := MediaInfo{
					Title:        title,
					DurationSec:  int64(parsed.Duration),
					ThumbnailURL: thumb,
				}
				// A playlist entry's info would make --load-info-json download only that entry,
				// while the URL downloads the whole playlist; only cache single-video metadata.
				if parsed.PlaylistID == nil && parsed.PlaylistIndex == nil {
					info.RawInfoJSON = append([]byte(nil), trimmed...)
				}
				return info, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return MediaInfo{}, err
		}
	}
	return MediaInfo{}, ErrNoMediaInfo
}

// validateURL ensures the URL is safe to pass to external commands
func validateURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("empty URL")
	}
	if len(rawURL) > 2048 {
		return fmt.Errorf("URL too long")
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
		return fmt.Errorf("missing host")
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
		return fmt.Errorf("URL contains line breaks")
	}
	return nil
}
