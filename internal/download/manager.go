package download

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State string

const (
	StateQueued      State = "queued"
	StateDownloading State = "downloading"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
)

// Default yt-dlp format selection used when none is specified via Manager
// or VIDEOFETCH_YTDLP_FORMAT environment variable. Prefer any best streams to
// maximize success (may require ffmpeg for merging).
const defaultYTDLPFormat = "bestvideo*+bestaudio/best"

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

	// optional yt-dlp format selector (passed as -f). If empty, falls back to
	// env var VIDEOFETCH_YTDLP_FORMAT and then to a built-in default.
	ytdlpFormat string

	// optional yt-dlp impersonation client (passed as --impersonate). If empty,
	// falls back to env var VIDEOFETCH_YTDLP_IMPERSONATE; if still empty, not set.
	ytdlpImpersonate string

	hooks Hooks
}

// NewManager creates a download manager with a worker pool and a bounded queue.
func NewManager(outputDir string, workers, queueCap int) *Manager {
	return NewManagerWithOptions(outputDir, workers, queueCap, ManagerOptions{})
}

// ManagerOptions configures yt-dlp invocation behavior.
type ManagerOptions struct {
	Format      string
	Impersonate string
	Hooks       Hooks
}

// NewManagerWithFormat is like NewManager but allows specifying a yt-dlp format selector.
func NewManagerWithFormat(outputDir string, workers, queueCap int, ytdlpFormat string) *Manager {
	return NewManagerWithOptions(outputDir, workers, queueCap, ManagerOptions{Format: ytdlpFormat})
}

// NewManagerWithOptions allows specifying format and impersonation.
func NewManagerWithOptions(outputDir string, workers, queueCap int, opts ManagerOptions) *Manager {
	if workers <= 0 {
		workers = max(runtime.NumCPU(), 1)
	}
	if queueCap <= 0 {
		queueCap = 64
	}
	m := &Manager{
		outDir:           outputDir,
		jobs:             make(chan job, queueCap),
		downloads:        make(map[string]*Item, queueCap*2),
		ytdlpFormat:      opts.Format,
		ytdlpImpersonate: opts.Impersonate,
		hooks:            opts.Hooks,
	}
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.worker(i)
	}
	return m
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
	// Output template
	outTpl := filepath.Join(m.outDir, "%(title)s-%(id)s.%(ext)s")

	// Helper to run yt-dlp once with given options
	runOnce := func(format, impersonate string) error {
		// Align with the Rust example: use a simple, parseable progress template
		// that emits to stdout lines beginning with "remedia-" and include
		// downloaded/total/estimate/eta. Also embed metadata/thumbnail/chapters
		// and continue partially downloaded files.
		args := []string{
			"--newline", "--no-color", "--no-playlist",
			"--progress-template", "download:remedia-%(progress.downloaded_bytes)s-%(progress.total_bytes)s-%(progress.total_bytes_estimate)s-%(progress.eta)s",
			"--continue",
			"--embed-thumbnail", "--embed-metadata", "--embed-chapters", "--windows-filenames",
			"-o", outTpl, url,
		}
		if format != "" {
			base := []string{"-f", format}
			if len(args) >= 3 {
				args = append(args[:len(args)-3], append(base, args[len(args)-3:]...)...)
			} else {
				args = append(base, args...)
			}
		}
		if impersonate != "" {
			args = append([]string{"--impersonate", impersonate}, args...)
		}
		cmd := exec.Command("yt-dlp", args...)

		// Progress appears on stderr; capture both to be safe and tee into buffers for diagnostics.
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

		// Read progress concurrently.
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
			// Include tail of stderr for better diagnostics
			tail := tailString(stderrBuf.String(), 512)
			if tail != "" {
				return fmt.Errorf("yt-dlp: %w: %s", err, tail)
			}
			return fmt.Errorf("yt-dlp: %w", err)
		}
		return nil
	}

	// Initial selection precedence: Manager field > env > default
	format := m.ytdlpFormat
	if format == "" {
		format = os.Getenv("VIDEOFETCH_YTDLP_FORMAT")
	}
	if format == "" {
		format = defaultYTDLPFormat
	}
	imp := m.ytdlpImpersonate
	if imp == "" {
		imp = os.Getenv("VIDEOFETCH_YTDLP_IMPERSONATE")
	}

	log.Printf("yt-dlp start id=%s url=%s format=%q impersonate=%q output=%s", id, url, format, imp, outTpl)
	if err := runOnce(format, imp); err != nil {
		emsg := err.Error()
		if shouldFallback(emsg) {
			// Try a sequence of safer formats. First allow merging of best video+audio
			// (requires ffmpeg). Then try common progressive/pre-merged fallbacks.
			fallbackFormats := []string{
				"bestvideo*+bestaudio/best",
				"22/18/b",
				"b/18",
			}
			for _, ff := range fallbackFormats {
				fbImp := imp
				if fbImp == "" {
					fbImp = detectBestImpersonation()
				}
				log.Printf("yt-dlp failed for %s; retrying with fallback: -f %q --impersonate %q", id, ff, fbImp)
				if err2 := runOnce(ff, fbImp); err2 != nil {
					lower := strings.ToLower(err2.Error())
					// If impersonation isn't supported in this environment, retry without it
					if strings.Contains(lower, "impersonate target") {
						log.Printf("impersonation %q unavailable; retrying fallback without impersonation", fbImp)
						if err3 := runOnce(ff, ""); err3 == nil {
							log.Printf("yt-dlp success id=%s format=%q impersonate=%q (fallback no-imp)", id, ff, "")
							return nil
						} else {
							// continue to next fallback
							continue
						}
					}
					// If merging failed due to missing ffmpeg, try next (likely pre-merged)
					if strings.Contains(lower, "ffmpeg") || strings.Contains(lower, "post-processing") {
						continue
					}
					// If still hitting provider restrictions, continue to next fallback
					if shouldFallback(lower) {
						continue
					}
					// For other errors, abort early
					return err2
				} else {
					log.Printf("yt-dlp success id=%s format=%q impersonate=%q (fallback)", id, ff, fbImp)
					return nil
				}
			}
			// All fallbacks failed; return last error from initial attempt
			return fmt.Errorf("yt-dlp: all fallbacks failed: %s", tailString(emsg, 256))
		}
		return err
	}
	log.Printf("yt-dlp success id=%s url=%s format=%q impersonate=%q", id, url, format, imp)
	return nil
}

func (m *Manager) parseProgress(id string, sc *bufio.Scanner) {
	// Increase buffer to handle long lines
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
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
				go m.hooks.OnProgress(dbid, prog)
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
			go m.hooks.OnStateChange(dbid, state, errStr)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) updateFailure(id string, err error) {
	msg := err.Error()
	// reduce noise from long command errors
	if len(msg) > 512 {
		msg = msg[:512]
	}
	m.updateState(id, StateFailed, msg)
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

// shouldFallback returns true if the error text suggests we should retry with
// simpler/pre-merged formats and a more permissive client impersonation.
func shouldFallback(errText string) bool {
	et := strings.ToLower(errText)
	if strings.Contains(et, "http error 403") {
		return true
	}
	if strings.Contains(et, "fragment 1 not found") {
		return true
	}
	if strings.Contains(et, "requested format is not available") {
		return true
	}
	// Generic "unable to continue" often accompanies the above
	if strings.Contains(et, "unable to continue") {
		return true
	}
	return false
}

// detectBestImpersonation inspects yt-dlp's available impersonation targets and
// returns a preferred target string (e.g., "chrome-131:windows-10"). Returns
// empty string if detection fails.
func detectBestImpersonation() string {
	out, err := exec.Command("yt-dlp", "--list-impersonate-targets").CombinedOutput()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	type cand struct {
		ver        int
		os, client string
	}
	bestRank := -1
	bestVer := -1
	var best cand
	// Preference order for OS
	rankOS := func(os string) int {
		os = strings.ToLower(os)
		switch os {
		case "windows-10":
			return 5
		case "macos-15":
			return 4
		case "macos-14":
			return 3
		case "android-14":
			return 2
		default:
			return 1
		}
	}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "[") || strings.HasPrefix(ln, "Client") || strings.HasPrefix(ln, "-") {
			continue
		}
		// Expect columns: Client  OS  Source
		fields := strings.Fields(ln)
		if len(fields) < 2 {
			continue
		}
		client := strings.ToLower(fields[0]) // e.g., chrome-131
		osName := strings.ToLower(fields[1]) // e.g., windows-10
		if !strings.HasPrefix(client, "chrome") {
			continue
		}
		ver := 0
		if i := strings.Index(client, "-"); i >= 0 && i+1 < len(client) {
			if n, err := strconv.Atoi(client[i+1:]); err == nil {
				ver = n
			}
		}
		r := rankOS(osName)
		if r > bestRank || (r == bestRank && ver > bestVer) {
			bestRank = r
			bestVer = ver
			best = cand{ver: ver, os: osName, client: client}
		}
	}
	if bestRank <= 0 {
		return ""
	}
	// Compose as "chrome-<ver>:<os>" if version known; else "chrome:<os>"
	base := "chrome"
	if best.ver > 0 {
		base = fmt.Sprintf("chrome-%d", best.ver)
	}
	return fmt.Sprintf("%s:%s", base, best.os)
}
