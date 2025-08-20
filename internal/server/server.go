package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/ui"
)

type downloadManager interface {
	Enqueue(url string) (string, error)
	Snapshot(id string) []*download.Item
}

type rateLimiter interface {
	Allow(key string) bool
}

// New returns an http.Handler with routes and middleware wired.
func New(mgr downloadManager) http.Handler {
	rl := newIPRateLimiter(60, time.Minute) // 60 req/min/IP
	mux := http.NewServeMux()

	// Routes
	mux.HandleFunc("/api/download_single", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_request"})
			return
		}
		if !validURL(req.URL) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_url"})
			return
		}
		id, err := mgr.Enqueue(req.URL)
		if err != nil {
			switch err.Error() {
			case "queue_full":
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"status": "error", "message": "queue_full"})
			case "shutting_down":
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "message": "shutting_down"})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued", "id": id})
	}))

	mux.HandleFunc("/api/download", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req struct {
			URLs []string `json:"urls"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil || len(req.URLs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_request"})
			return
		}
		ids := make([]string, 0, len(req.URLs))
		for _, u := range req.URLs {
			if !validURL(u) {
				continue
			}
			id, err := mgr.Enqueue(u)
			if err != nil {
				log.Printf("enqueue error for %s: %v", u, err)
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "no_valid_urls"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued", "ids": ids})
	}))

	mux.HandleFunc("/api/status", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		id := r.URL.Query().Get("id")
		items := mgr.Snapshot(id)
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "downloads": items})
	}))

	// Dashboard (HTML via Templ + HTMX)
	mux.HandleFunc("/", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/dashboard" {
			// Let other handlers handle or 404; fallthrough by returning
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		items := mgr.Snapshot("")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.Dashboard(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		items := mgr.Snapshot("")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.Dashboard(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard/rows", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		items := mgr.Snapshot("")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.QueueTable(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard/enqueue", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid form"))
			return
		}
		u := strings.TrimSpace(r.Form.Get("url"))
		if !validURL(u) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid url"))
			return
		}
		if _, err := mgr.Enqueue(u); err != nil {
			// Map known errors to user-facing messages
			switch err.Error() {
			case "queue_full":
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("queue full"))
				return
			case "shutting_down":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("shutting down"))
			default:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
				return
			}
		}
		// Redirect back to dashboard so the HTMX poll refreshes
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}))

	// Static files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

	// Healthcheck
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Add minimal logging + recover
	return recoverer(logger(mux))
}

// Utilities

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"status": "error", "message": "method_not_allowed"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func validURL(u string) bool {
	if len(u) == 0 || len(u) > 2048 { // sanity cap
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return true
}

// Middleware

func with(rl rateLimiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.Allow(ip) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"status": "error", "message": "rate_limited"})
			return
		}
		h(w, r)
	}
}

func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %s", r.RemoteAddr, r.Method, r.URL.Path, time.Since(start))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic: %v", v)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	// Respect common proxy headers, then fall back to RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// Simple token bucket per IP with fixed refill interval and capacity.
type ipRateLimiter struct {
	cap     int
	refill  time.Duration
	buckets map[string]*bucket
	// protect buckets
	mu sync.Mutex
}

type bucket struct {
	tokens int
	last   time.Time
}

func newIPRateLimiter(cap int, refill time.Duration) *ipRateLimiter {
	return &ipRateLimiter{cap: cap, refill: refill, buckets: make(map[string]*bucket)}
}

func (rl *ipRateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b := rl.buckets[key]
	if b == nil {
		b = &bucket{tokens: rl.cap - 1, last: now}
		rl.buckets[key] = b
		return true
	}
	// refill if interval passed
	if d := now.Sub(b.last); d >= rl.refill {
		// reset once per interval
		b.tokens = rl.cap
		b.last = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
