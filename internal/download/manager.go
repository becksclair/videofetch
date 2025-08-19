package download

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

type Item struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Progress float64 `json:"progress"` // 0-100
	State    State   `json:"state"`
	Error    string  `json:"error,omitempty"`

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
}

// NewManager creates a download manager with a worker pool and a bounded queue.
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
	_, err := exec.LookPath("yt-dlp")
	return err
}

// runYTDLP invokes yt-dlp and parses progress output to update the item.
func (m *Manager) runYTDLP(id, url string) error {
	// Defensive: ensure yt-dlp exists.
	if err := CheckYTDLP(); err != nil {
		return fmt.Errorf("yt_dlp_not_found: %w", err)
	}
	// Output template
	outTpl := filepath.Join(m.outDir, "%(title)s-%(id)s.%(ext)s")
	// --newline prints a new line for progress updates; progress is on stderr.
	args := []string{"--newline", "--no-color", "--no-playlist", "-o", outTpl, url}
	cmd := exec.Command("yt-dlp", args...)

	// Progress appears on stderr; capture both to be safe.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Read progress concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		m.parseProgress(id, bufio.NewScanner(stderr))
	}()
	go func() {
		defer wg.Done()
		m.parseProgress(id, bufio.NewScanner(stdout))
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("yt-dlp: %w", err)
	}
	return nil
}

var percentRe = regexp.MustCompile(`(?i)(\d{1,3}(?:\.\d+)?)%`)

func (m *Manager) parseProgress(id string, sc *bufio.Scanner) {
	// Increase buffer to handle long lines
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// Quick path: only update on lines that look like progress
		if !strings.Contains(line, "%") {
			continue
		}
		match := percentRe.FindStringSubmatch(line)
		if len(match) >= 2 {
			p := parsePercent(match[1])
			if p >= 0 && p <= 100 {
				m.updateProgress(id, p)
			}
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("progress scan error for %s: %v", id, err)
	}
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
			it.Progress = p
			it.updatedAt = time.Now()
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
