package download

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type State string

const (
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
)

// Buffer pool for progress parsing to reduce allocations
var progressBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 4096) // 4KB initial size
		return &buf
	},
}

type Item struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Progress float64 `json:"progress"` // 0-100
	State    State   `json:"state"`
	Error    string  `json:"error,omitempty"`

	// Optional metadata for UI convenience.
	Title        string `json:"title,omitempty"`
	Duration     int64  `json:"duration,omitempty"` // seconds
	ThumbnailURL string `json:"thumbnail_url,omitempty"`

	// Optional database binding for persistence updates.
	DBID int64 `json:"db_id,omitempty"`
	
	// Filename gets set when download is complete.
	Filename string `json:"filename,omitempty"`

	startedAt time.Time
	updatedAt time.Time
}

type job struct {
	id  string
	url string
}

type Manager struct {
	outDir string

	jobs    chan job
	wg      sync.WaitGroup
	closing atomic.Bool

	mu        sync.RWMutex
	downloads map[string]*Item

	hooks Hooks
}

func NewManager(outputDir string, workers, queueCap int) *Manager {
	if workers <= 0 {
		workers = max(runtime.NumCPU(), 1)
	}
	if queueCap <= 0 {
		queueCap = 64
	}
	m := &Manager{
		outDir:    outputDir,
		jobs:      make(chan job, queueCap),
		downloads: make(map[string]*Item, queueCap*2),
	}
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
	return m
}

// SetHooks configures the hooks for progress and state updates
func (m *Manager) SetHooks(hooks Hooks) {
	m.hooks = hooks
}

// StopAccepting stops queueing new jobs; Enqueue will return an error afterwards.
func (m *Manager) StopAccepting() {
	m.closing.Store(true)
}

// Shutdown cancels workers after current job; safe to call multiple times.
func (m *Manager) Shutdown() {
	if m.closing.Swap(true) {
		// was already true
	}
	close(m.jobs)
	m.wg.Wait()
}

// Enqueue adds a new URL to the queue and returns the assigned ID.
func (m *Manager) Enqueue(url string) (string, error) {
	if m.closing.Load() {
		return "", errors.New("shutting_down")
	}
	id := genID()
	it := &Item{ID: id, URL: url, Progress: 0, State: StateQueued, startedAt: time.Now(), updatedAt: time.Now()}
	m.mu.Lock()
	m.downloads[id] = it
	m.mu.Unlock()

	select {
	case m.jobs <- job{id: id, url: url}:
		return id, nil
	default:
		// queue full
		// remove the entry we just added
		m.mu.Lock()
		delete(m.downloads, id)
		m.mu.Unlock()
		return "", errors.New("queue_full")
	}
}

// AttachDB binds a database row ID to the in-memory item for persistence updates.
func (m *Manager) AttachDB(id string, dbID int64) {
	m.mu.Lock()
	if it, ok := m.downloads[id]; ok {
		it.DBID = dbID
	}
	m.mu.Unlock()
}

// SetMeta updates the in-memory item with extracted metadata for UI.
func (m *Manager) SetMeta(id string, title string, duration int64, thumb string) {
	m.mu.Lock()
	if it, ok := m.downloads[id]; ok {
		if title != "" {
			it.Title = title
		}
		if duration > 0 {
			it.Duration = duration
		}
		if thumb != "" {
			it.ThumbnailURL = thumb
		}
		it.updatedAt = time.Now()
	}
	m.mu.Unlock()
}

// Snapshot returns a copy of the current download items. If id is non-empty, returns at most that item.
func (m *Manager) Snapshot(id string) []*Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id != "" {
		if it, ok := m.downloads[id]; ok {
			cp := *it
			return []*Item{&cp}
		}
		return []*Item{}
	}
	out := make([]*Item, 0, len(m.downloads))
	for _, it := range m.downloads {
		cp := *it
		out = append(out, &cp)
	}
	return out
}

func (m *Manager) worker(idx int) {
	defer m.wg.Done()
	for j := range m.jobs {
		m.updateState(j.id, StateDownloading, "")
		if err := m.runYTDLP(j.id, j.url); err != nil {
			m.updateFailure(j.id, err)
		} else {
			m.updateProgress(j.id, 100)
			m.updateState(j.id, StateCompleted, "")
		}
	}
}

// CheckYTDLP ensures yt-dlp is in PATH.
func CheckYTDLP() error {
	// Ensure yt-dlp exists
	p, err := exec.LookPath("yt-dlp")
	if err != nil {
		return err
	}
	// Ensure it supports --progress-template so our parser remains stable.
	// If the flag is not supported, yt-dlp --help will not contain it.
	out, err := exec.Command(p, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("yt-dlp not runnable: %w", err)
	}
	if !strings.Contains(string(out), "--progress-template") {
		return fmt.Errorf("yt_dlp_outdated: missing --progress-template support")
	}
	return nil
}

// runYTDLP invokes yt-dlp and parses progress output to update the item.
func (m *Manager) runYTDLP(id, url string) error {
	// Defensive: ensure yt-dlp exists.
	if err := CheckYTDLP(); err != nil {
		return fmt.Errorf("yt_dlp_not_found: %w", err)
	}

	outTpl := filepath.Join(m.outDir, "%(title).200s-%(id)s.%(ext)s")

	log.Printf("yt-dlp start id=%s url=%s output=%s", id, url, outTpl)

	args := m.buildYTDLPArgs(url, outTpl)
	cmd := exec.Command("yt-dlp", args...)

	if err := m.executeWithProgressTracking(id, cmd); err != nil {
		return err
	}

	log.Printf("yt-dlp success id=%s url=%s", id, url)
	return nil
}

// buildYTDLPArgs constructs the argument list for yt-dlp based on Rust reference
func (m *Manager) buildYTDLPArgs(url, outTpl string) []string {
	return []string{
		url,
		"--progress-template", "download:remedia-%(progress.downloaded_bytes)s-%(progress.total_bytes)s-%(progress.total_bytes_estimate)s-%(progress.eta)s",
		"--newline",
		"--continue",
		"--output", outTpl,
		"--embed-thumbnail",
		"--embed-subs",
		"--embed-metadata",
		"--embed-chapters",
		"--windows-filenames",
	}
}

// executeWithProgressTracking runs the command and tracks progress
func (m *Manager) executeWithProgressTracking(id string, cmd *exec.Cmd) error {
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
		m.parseProgress(id, bufio.NewScanner(io.TeeReader(stderr, &stderrBuf)))
	}()
	go func() {
		defer wg.Done()
		m.parseProgress(id, bufio.NewScanner(io.TeeReader(stdout, &stdoutBuf)))
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		tail := tailString(stderrBuf.String(), 512)
		if tail != "" {
			return fmt.Errorf("yt-dlp: %w: %s", err, tail)
		}
		return fmt.Errorf("yt-dlp: %w", err)
	}

	// Extract filename from stdout after successful completion
	stdoutStr := strings.TrimSpace(stdoutBuf.String())
	if filename := m.extractFilename(stdoutStr); filename != "" {
		m.setFilename(id, filename)
	}

	return nil
}

func (m *Manager) parseProgress(id string, sc *bufio.Scanner) {
	// Use buffer pool to reduce allocations
	bufPtr := progressBufferPool.Get().(*[]byte)
	defer progressBufferPool.Put(bufPtr)

	// Set a reasonable max buffer size (256KB)
	sc.Buffer(*bufPtr, 256*1024)
	// Split on either \n, \r\n, or bare \r since yt-dlp often rewrites
	// progress on the same line using carriage returns.
	sc.Split(scanCRorLF)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Only handle lines from our progress template. Rust example uses
		// prefix "remedia-<downloaded>-<total>-<estimate>-<eta>".
		if !strings.HasPrefix(line, "remedia-") {
			continue
		}
		parts := strings.Split(line, "-")
		if len(parts) < 5 {
			continue
		}
		// parts[0] = "remedia"
		downloaded := parseFloat64(parts[1])
		total := parseFloat64(parts[2])
		estimate := parseFloat64(parts[3])
		// parts[4] is eta; unused for now
		tBytes := total
		if tBytes <= 0 && estimate > 0 {
			tBytes = estimate
		}
		if tBytes > 0 && downloaded >= 0 {
			p := downloaded / tBytes * 100.0
			if p >= 100 {
				p = 99
			}
			if p >= 0 {
				m.updateProgress(id, p)
			}
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("progress scan error for %s: %v", id, err)
	}
}

// parseFloat64 parses a simple decimal number, returning -1 on error.
func parseFloat64(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	// Avoid introducing strconv allocations; manual parse is fine here.
	var whole int64
	var frac int64
	var fracPow float64 = 1
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	dotSeen := false
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' && !dotSeen {
			dotSeen = true
			continue
		}
		if c < '0' || c > '9' {
			return -1
		}
		d := int64(c - '0')
		if !dotSeen {
			whole = whole*10 + d
		} else {
			frac = frac*10 + d
			fracPow *= 10
		}
	}
	val := float64(whole)
	if fracPow > 1 {
		val += float64(frac) / fracPow
	}
	if neg {
		val = -val
	}
	return val
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

func parsePercent(s string) float64 {
	// acceptable formats: "12", "12.3"
	var whole, frac int
	var n int
	if i := strings.IndexByte(s, '.'); i >= 0 {
		// fractional
		wholeStr := s[:i]
		fracStr := s[i+1:]
		for _, r := range wholeStr {
			if r < '0' || r > '9' {
				return -1
			}
			whole = whole*10 + int(r-'0')
		}
		pow := 1.0
		for _, r := range fracStr {
			if r < '0' || r > '9' {
				break
			}
			frac = frac*10 + int(r-'0')
			pow *= 10
		}
		return float64(whole) + float64(frac)/pow
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return float64(n)
}

func (m *Manager) updateProgress(id string, p float64) {
	m.mu.Lock()
	if it, ok := m.downloads[id]; ok {
		// only increase progress (yt-dlp prints for multiple phases)
		if p > it.Progress {
			prev := it.Progress
			it.Progress = p
			it.updatedAt = time.Now()
			// Log when integer percentage advances to reduce noise
			if int(p) != int(prev) {
				log.Printf("yt-dlp progress id=%s url=%s progress=%d%%", id, it.URL, int(p))
			}
			if it.DBID > 0 && m.hooks != nil {
				dbid := it.DBID
				prog := it.Progress
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					m.callHookWithTimeout(ctx, func() { m.hooks.OnProgress(dbid, prog) })
				}()
			}
		}
	}
	m.mu.Unlock()
}

func (m *Manager) updateState(id string, st State, errMsg string) {
	m.mu.Lock()
	if it, ok := m.downloads[id]; ok {
		it.State = st
		it.Error = errMsg
		it.updatedAt = time.Now()
		log.Printf("download state id=%s url=%s state=%s", id, it.URL, st)
		if it.DBID > 0 && m.hooks != nil {
			dbid := it.DBID
			state := st
			errStr := errMsg
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				m.callHookWithTimeout(ctx, func() { m.hooks.OnStateChange(dbid, state, errStr) })
			}()
		}
	}
	m.mu.Unlock()
}

func (m *Manager) updateFailure(id string, err error) {
	msg := err.Error()
	// reduce noise from long command errors, respecting UTF-8 boundaries
	msg = truncateUTF8(msg, 512)
	m.updateState(id, StateFailed, msg)
}

// extractFilename extracts the downloaded filename from yt-dlp output
func (m *Manager) extractFilename(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for the destination line that shows where the file was saved
		if strings.Contains(line, "Destination:") {
			// Extract filename from "Destination: /path/to/file.ext"
			parts := strings.SplitN(line, "Destination:", 2)
			if len(parts) == 2 {
				path := strings.TrimSpace(parts[1])
				return filepath.Base(path)
			}
		}
		// Also check for merge output lines like "Merging formats into 'filename'"
		if strings.Contains(line, "Merging formats into") {
			start := strings.Index(line, "'")
			if start != -1 {
				end := strings.Index(line[start+1:], "'")
				if end != -1 {
					path := line[start+1 : start+1+end]
					return filepath.Base(path)
				}
			}
		}
		// Check for final output lines with [download] tag
		if strings.HasPrefix(line, "[download]") && strings.Contains(line, "has already been downloaded") {
			// Extract from "[download] filename has already been downloaded"
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return filepath.Base(parts[1])
			}
		}
	}
	return ""
}

// setFilename updates the filename for an item and calls the hook
func (m *Manager) setFilename(id, filename string) {
	m.mu.Lock()
	if it, ok := m.downloads[id]; ok {
		it.Filename = filename
		it.updatedAt = time.Now()
		log.Printf("download filename id=%s filename=%s", id, filename)
		if it.DBID > 0 && m.hooks != nil {
			dbid := it.DBID
			fname := filename
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				m.callHookWithTimeout(ctx, func() { m.hooks.OnFilename(dbid, fname) })
			}()
		}
	}
	m.mu.Unlock()
}

func genID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// fallback
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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

// truncateUTF8 truncates a string to at most n bytes while preserving UTF-8 validity
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}

	// Start from position n and work backwards to find a valid UTF-8 boundary
	// A valid UTF-8 boundary is either at position 0 or where the byte is not a continuation byte
	for i := n; i >= 0; i-- {
		// UTF-8 continuation bytes have the form 10xxxxxx (0x80-0xBF)
		// A valid boundary is where we're not in the middle of a multi-byte sequence
		if i == 0 || (s[i]&0xC0) != 0x80 {
			// Verify this is actually a valid UTF-8 string
			if utf8.ValidString(s[:i]) {
				return s[:i]
			}
		}
	}
	// Fallback: empty string if no valid UTF-8 found
	return ""
}

// callHookWithTimeout executes a hook function with context timeout protection
func (m *Manager) callHookWithTimeout(ctx context.Context, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-ctx.Done():
		// Hook timed out, but we can't cancel it - just log and continue
		log.Printf("hook call timed out")
	case <-done:
		// Hook completed successfully
	}
}
