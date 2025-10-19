package download

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"videofetch/internal/logging"
)

// Downloader executes yt-dlp downloads with progress tracking.
// It encapsulates all yt-dlp subprocess management and output parsing.
type Downloader struct {
	outDir string

	// Callbacks for progress and filename updates
	onProgress func(id string, progress float64)
	onFilename func(id string, filename string)
}

// NewDownloader creates a new Downloader with the specified output directory and callbacks.
func NewDownloader(outputDir string) *Downloader {
	return &Downloader{
		outDir: outputDir,
	}
}

// SetProgressCallback sets the callback for progress updates.
func (d *Downloader) SetProgressCallback(fn func(id string, progress float64)) {
	d.onProgress = fn
}

// SetFilenameCallback sets the callback for filename detection.
func (d *Downloader) SetFilenameCallback(fn func(id string, filename string)) {
	d.onFilename = fn
}

// Download executes a yt-dlp download for the given URL.
// It blocks until the download completes or fails.
func (d *Downloader) Download(ctx context.Context, id, url string) error {
	// Defensive: ensure yt-dlp exists.
	if err := CheckYTDLP(); err != nil {
		return fmt.Errorf("yt_dlp_not_found: %w", err)
	}

	outTpl := filepath.Join(d.outDir, "%(title).200s-%(id)s.%(ext)s")

	logging.LogYTDLPCommand(id, url, outTpl, false)

	args := buildYTDLPArgs(url, outTpl)
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)

	if err := d.executeWithProgressTracking(id, cmd); err != nil {
		return err
	}

	logging.LogYTDLPCommand(id, url, outTpl, true)
	return nil
}

// buildYTDLPArgs constructs the argument list for yt-dlp based on Rust reference
func buildYTDLPArgs(url, outTpl string) []string {
	return []string{
		url,
		"--progress-template", "download:%(progress)j",
		"--newline",
		"--continue",
		"--output", outTpl,
		"--embed-thumbnail",
		// "--embed-subs",
		"--embed-metadata",
		"--embed-chapters",
		"--windows-filenames",
	}
}

// executeWithProgressTracking runs the command and tracks progress
func (d *Downloader) executeWithProgressTracking(id string, cmd *exec.Cmd) error {
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout: %w", err)
	}

	var stderrBuf, stdoutBuf bytes.Buffer

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Read progress concurrently
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		d.parseProgress(id, bufio.NewScanner(io.TeeReader(stderr, &stderrBuf)))
	}()
	go func() {
		defer wg.Done()
		d.parseProgress(id, bufio.NewScanner(io.TeeReader(stdout, &stdoutBuf)))
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		tail := tailString(stderrBuf.String(), 512)
		if tail != "" {
			return fmt.Errorf("yt-dlp: %w: %s", err, tail)
		}
		return fmt.Errorf("yt-dlp: %w", err)
	}

	// Extract filename from combined output (some yt-dlp messages go to stderr)
	combined := strings.TrimSpace(stdoutBuf.String() + "\n" + stderrBuf.String())
	if filename := extractFilename(combined); filename != "" {
		if d.onFilename != nil {
			d.onFilename(id, filename)
		}
	}

	return nil
}

// parseProgress parses yt-dlp progress output and calls the progress callback
func (d *Downloader) parseProgress(id string, sc *bufio.Scanner) {
	// Set a reasonable max buffer size (256KB)
	// Scanner manages the buffer internally, so we don't need a pool
	sc.Buffer(make([]byte, 4096), 256*1024)
	// Split on either \n, \r\n, or bare \r since yt-dlp often rewrites
	// progress on the same line using carriage returns.
	sc.Split(scanCRorLF)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		// Try to parse as JSON
		var progress progressData
		if err := json.Unmarshal([]byte(line), &progress); err != nil {
			// Not JSON, skip this line (could be other yt-dlp output)
			continue
		}

		// Only process if we have valid download progress
		if progress.Status != "downloading" {
			continue
		}

		// Calculate percentage from bytes
		downloaded := progress.DownloadedBytes
		total := progress.TotalBytes

		// Use estimate if total is not available
		if total <= 0 && progress.TotalBytesEstimate > 0 {
			total = progress.TotalBytesEstimate
		}

		// Calculate and update progress percentage
		if total > 0 && downloaded >= 0 {
			p := downloaded / total * 100.0
			// Cap percentage to [0,100]
			if p > 100 {
				p = 100
			} else if p < 0 {
				p = 0
			}
			if d.onProgress != nil {
				d.onProgress(id, p)
			}
		}
	}
	if err := sc.Err(); err != nil {
		logging.LogProgressScanError(id, err)
	}
}

// extractFilename extracts the downloaded filename from yt-dlp output
func extractFilename(output string) string {
	lines := strings.Split(output, "\n")
	var (
		mergedName      string
		alreadyDLName   string
		lastDestination string
	)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Prefer explicit final filename from merger stage
		if strings.Contains(line, "Merging formats into") {
			// Support both single and double quotes
			// Example: [Merger] Merging formats into 'Title-id.mp4'
			// or: [Merger] Merging formats into "Title-id.mp4"
			start := strings.IndexAny(line, "'\"")
			if start != -1 {
				quote := line[start]
				rest := line[start+1:]
				if end := strings.IndexByte(rest, quote); end != -1 {
					mergedName = filepath.Base(rest[:end])
					continue
				}
			}
		}
		// If yt-dlp says file already exists, that includes the final filename
		if strings.HasPrefix(line, "[download]") && strings.Contains(line, "has already been downloaded") {
			// Format: [download] Title-id.mp4 has already been downloaded
			// After prefix, take the segment before " has already"
			// Fall back to fields if needed
			if i := strings.Index(line, "] "); i != -1 {
				rest := line[i+2:]
				if j := strings.Index(rest, " has already been downloaded"); j != -1 {
					alreadyDLName = filepath.Base(strings.TrimSpace(rest[:j]))
					continue
				}
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				alreadyDLName = filepath.Base(parts[1])
				continue
			}
		}
		// Record destination lines as a fallback (may be intermediate e.g., fXXX selections)
		if strings.Contains(line, "Destination:") {
			parts := strings.SplitN(line, "Destination:", 2)
			if len(parts) == 2 {
				path := strings.TrimSpace(parts[1])
				lastDestination = filepath.Base(path)
				continue
			}
		}
	}
	switch {
	case mergedName != "":
		return mergedName
	case alreadyDLName != "":
		return alreadyDLName
	case lastDestination != "":
		return lastDestination
	default:
		return ""
	}
}

// scanCRorLF is like bufio.ScanLines but treats a bare '\r' as a line
// terminator as well. It also handles CRLF and strips a trailing CR.
func scanCRorLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// If at EOF and no data, return no token
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	// Search for newline or carriage return
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			// Return the line without the trailing CR, if present
			line := data[:i]
			if i > 0 && data[i-1] == '\r' {
				line = data[:i-1]
			}
			return i + 1, line, nil
		}
		if data[i] == '\r' {
			// If CRLF, consume both; else just CR
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
	}
	// If at EOF, return the remaining data.
	if atEOF {
		// Drop a trailing CR, if any
		if len(data) > 0 && data[len(data)-1] == '\r' {
			return len(data), data[:len(data)-1], nil
		}
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

// tailString returns the last at most n bytes from s (by rune boundary best-effort).
func tailString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[len(s)-n:])
}
