package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/store"
	"videofetch/internal/ui"
)

type downloadManager interface {
	Enqueue(url string) (string, error)
	Snapshot(id string) []*download.Item
	AttachDB(id string, dbID int64)
	SetMeta(id string, title string, duration int64, thumb string)
}

type rateLimiter interface {
	Allow(key string) bool
}

// New returns an http.Handler with routes and middleware wired.
// Minimal interface to abstract the store; nil store disables DB-backed features.
func New(mgr downloadManager, st *store.Store, outputDir string) http.Handler {
	rl := newIPRateLimiter(60, time.Minute) // 60 req/min/IP
	mux := http.NewServeMux()
	// helpers
	var storeCreate func(ctx context.Context, url, title string, duration int64, thumbnail string, status string, progress float64) (int64, error)
	var storeUpdateMeta func(ctx context.Context, id int64, title string, duration int64, thumbnail string) error
	if st != nil {
		storeCreate = st.CreateDownload
		storeUpdateMeta = st.UpdateMeta
	}

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
		// If store available, prefetch media info and create DB record first
		var dbid int64
		var title string
		var dur int64
		var thumb string
		if storeCreate != nil {
			if mi, err := download.FetchMediaInfo(req.URL); err == nil {
				title, dur, thumb = mi.Title, mi.DurationSec, mi.ThumbnailURL
			} else {
				// Fallbacks: still create a record with URL as title
				title = req.URL
			}
			if idv, err := storeCreate(r.Context(), req.URL, title, dur, thumb, "pending", 0); err == nil {
				dbid = idv
			} else {
				log.Printf("db create error: %v", err)
			}
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
		if dbid > 0 {
			mgr.AttachDB(id, dbid)
			if title != "" || dur > 0 || thumb != "" {
				mgr.SetMeta(id, title, dur, thumb)
				if storeUpdateMeta != nil {
					_ = storeUpdateMeta(r.Context(), dbid, title, dur, thumb)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued", "id": id, "db_id": dbid})
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
		dbIDs := make([]int64, 0, len(req.URLs))
		for _, u := range req.URLs {
			if !validURL(u) {
				continue
			}
			var dbid int64
			var title string
			var dur int64
			var thumb string
			if storeCreate != nil {
				if mi, err := download.FetchMediaInfo(u); err == nil {
					title, dur, thumb = mi.Title, mi.DurationSec, mi.ThumbnailURL
				} else {
					title = u
				}
				if idv, err := storeCreate(r.Context(), u, title, dur, thumb, "pending", 0); err == nil {
					dbid = idv
				} else {
					log.Printf("db create error: %v", err)
				}
			}
			id, err := mgr.Enqueue(u)
			if err != nil {
				log.Printf("enqueue error for %s: %v", u, err)
				continue
			}
			ids = append(ids, id)
			if dbid > 0 {
				mgr.AttachDB(id, dbid)
				if title != "" || dur > 0 || thumb != "" {
					mgr.SetMeta(id, title, dur, thumb)
				}
				dbIDs = append(dbIDs, dbid)
			}
		}
		if len(ids) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "no_valid_urls"})
			return
		}
		if len(dbIDs) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued", "ids": ids, "db_ids": dbIDs})
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

	// Optional DB-backed listing; only registered if store is provided via main.
	if st != nil {
		mux.HandleFunc("/api/remove", with(rl, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				methodNotAllowed(w)
				return
			}
			var req struct {
				ID int64 `json:"id"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.ID <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_request"})
				return
			}
			if err := st.DeleteDownload(r.Context(), req.ID); err != nil {
				log.Printf("delete download id=%d: %v", req.ID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "deleted"})
		}))

		mux.HandleFunc("/api/download_file", with(rl, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			idStr := r.URL.Query().Get("id")
			if idStr == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "missing_id"})
				return
			}
			var id int64
			if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_id"})
				return
			}
			
			// Get download record to find filename
			items, err := st.ListDownloads(r.Context(), store.ListFilter{})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			
			var filename string
			for _, item := range items {
				if item.ID == id {
					filename = item.Filename
					break
				}
			}
			
			if filename == "" {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "file_not_found"})
				return
			}
			
			// Check if file exists in output directory
			fullPath := filepath.Join(outputDir, filename)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "file_not_found"})
				return
			} else if err != nil {
				log.Printf("stat file %s: %v", fullPath, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			
			// Serve the file
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			http.ServeFile(w, r, fullPath)
		}))

		mux.HandleFunc("/api/downloads", with(rl, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			// Parse filters
			q := r.URL.Query()
			f := store.ListFilter{
				Status: q.Get("status"),
				Sort:   q.Get("sort"),
				Order:  q.Get("order"),
			}
			if lim := q.Get("limit"); lim != "" {
				// ignore conversion errors silently, relying on defaults
				// kept minimal, as this is a server-side admin API
			}
			items, err := st.ListDownloads(r.Context(), f)
			if err != nil {
				log.Printf("list downloads: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "downloads": items})
		}))
	}

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

	mux.HandleFunc("/dashboard-lcars", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		items := mgr.Snapshot("")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.DashboardLCARS(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard/rows", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		// Optional filter/sort controls
		q := r.URL.Query()
		status := strings.ToLower(strings.TrimSpace(q.Get("status")))
		sortBy := strings.ToLower(strings.TrimSpace(q.Get("sort")))
		order := strings.ToLower(strings.TrimSpace(q.Get("order")))

		var items []*download.Item
		if st != nil {
			// Prefer persisted listing when DB is enabled
			f := store.ListFilter{Status: status, Sort: sortBy, Order: order}
			rows, err := st.ListDownloads(r.Context(), f)
			if err != nil {
				log.Printf("list downloads: %v", err)
				rows = nil
			}
			items = make([]*download.Item, 0, len(rows))
			for i := range rows {
				d := rows[i]
				// Map DB status to in-memory state
				var stt download.State
				switch strings.ToLower(d.Status) {
				case "downloading":
					stt = download.StateDownloading
				case "completed":
					stt = download.StateCompleted
				case "error":
					stt = download.StateFailed
				default:
					stt = download.StateQueued
				}
				items = append(items, &download.Item{
					ID:           fmt.Sprintf("%d", d.ID),
					URL:          d.URL,
					Title:        d.Title,
					Duration:     d.Duration,
					ThumbnailURL: d.ThumbnailURL,
					Progress:     d.Progress,
					State:        stt,
					Filename:     d.Filename,
				})
			}
		} else {
			// Fallback: in-memory snapshot with basic filter/sort
			items = mgr.Snapshot("")
			if status != "" {
				filtered := make([]*download.Item, 0, len(items))
				for _, it := range items {
					if string(it.State) == status {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}
			if sortBy != "" {
				less := func(i, j int) bool { return false }
				switch sortBy {
				case "title":
					less = func(i, j int) bool {
						ai := items[i].Title
						if ai == "" {
							ai = items[i].URL
						}
						aj := items[j].Title
						if aj == "" {
							aj = items[j].URL
						}
						return strings.ToLower(ai) < strings.ToLower(aj)
					}
				case "status":
					less = func(i, j int) bool { return items[i].State < items[j].State }
				case "progress":
					less = func(i, j int) bool { return items[i].Progress < items[j].Progress }
				case "date":
					// No exported timestamps on snapshot items; ignore
					less = nil
				}
				if less != nil {
					if order == "desc" {
						sort.Slice(items, func(i, j int) bool { return less(j, i) })
					} else {
						sort.Slice(items, less)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.QueueTable(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard-lcars/rows", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		// Optional filter/sort controls
		q := r.URL.Query()
		status := strings.ToLower(strings.TrimSpace(q.Get("status")))
		sortBy := strings.ToLower(strings.TrimSpace(q.Get("sort")))
		order := strings.ToLower(strings.TrimSpace(q.Get("order")))

		var items []*download.Item
		if st != nil {
			// Prefer persisted listing when DB is enabled
			f := store.ListFilter{Status: status, Sort: sortBy, Order: order}
			rows, err := st.ListDownloads(r.Context(), f)
			if err != nil {
				log.Printf("list downloads: %v", err)
				rows = nil
			}
			items = make([]*download.Item, 0, len(rows))
			for i := range rows {
				d := rows[i]
				// Map DB status to in-memory state
				var stt download.State
				switch strings.ToLower(d.Status) {
				case "downloading":
					stt = download.StateDownloading
				case "completed":
					stt = download.StateCompleted
				case "error":
					stt = download.StateFailed
				default:
					stt = download.StateQueued
				}
				items = append(items, &download.Item{
					ID:           fmt.Sprintf("%d", d.ID),
					URL:          d.URL,
					Title:        d.Title,
					Duration:     d.Duration,
					ThumbnailURL: d.ThumbnailURL,
					Progress:     d.Progress,
					State:        stt,
					Filename:     d.Filename,
				})
			}
		} else {
			// Fallback: in-memory snapshot with basic filter/sort
			items = mgr.Snapshot("")
			if status != "" {
				filtered := make([]*download.Item, 0, len(items))
				for _, it := range items {
					if string(it.State) == status {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}
			if sortBy != "" {
				less := func(i, j int) bool { return false }
				switch sortBy {
				case "title":
					less = func(i, j int) bool {
						ai := items[i].Title
						if ai == "" {
							ai = items[i].URL
						}
						aj := items[j].Title
						if aj == "" {
							aj = items[j].URL
						}
						return strings.ToLower(ai) < strings.ToLower(aj)
					}
				case "status":
					less = func(i, j int) bool { return items[i].State < items[j].State }
				case "progress":
					less = func(i, j int) bool { return items[i].Progress < items[j].Progress }
				case "date":
					// No exported timestamps on snapshot items; ignore
					less = nil
				}
				if less != nil {
					if order == "desc" {
						sort.Slice(items, func(i, j int) bool { return less(j, i) })
					} else {
						sort.Slice(items, less)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.QueueTableLCARS(items).Render(context.Background(), w)
	}))

	mux.HandleFunc("/dashboard/enqueue", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Invalid form data</div>`))
			return
		}
		u := strings.TrimSpace(r.Form.Get("url"))
		if !validURL(u) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Invalid URL</div>`))
			return
		}

		// Enqueue immediately without metadata fetching to make it async
		id, err := mgr.Enqueue(u)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch err.Error() {
			case "queue_full":
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Queue is full, try again later</div>`))
			case "shutting_down":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Service is shutting down</div>`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Internal error</div>`))
			}
			return
		}

		// Create minimal DB record and fetch metadata asynchronously
		if storeCreate != nil {
			go func() {
				var dbid int64
				var title string
				var dur int64
				var thumb string
				
				// Create initial DB record with URL as title
				if idv, err := storeCreate(context.Background(), u, u, 0, "", "pending", 0); err == nil {
					dbid = idv
					mgr.AttachDB(id, dbid)
					
					// Fetch metadata asynchronously
					if mi, err := download.FetchMediaInfo(u); err == nil {
						title, dur, thumb = mi.Title, mi.DurationSec, mi.ThumbnailURL
						mgr.SetMeta(id, title, dur, thumb)
						if storeUpdateMeta != nil {
							_ = storeUpdateMeta(context.Background(), dbid, title, dur, thumb)
						}
					}
				} else {
					log.Printf("db create error: %v", err)
				}
			}()
		}

		// Return immediate success response with auto-clear and trigger queue refresh
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		
		// Generate response with success message and script to refresh queue
		response := `<div class="text-green-600 text-sm">✓ Video queued successfully <script>
			setTimeout(() => document.getElementById('enqueue-status').innerHTML = '', 3000);
			htmx.trigger('#queue', 'refresh');
		</script></div>`
		
		_, _ = w.Write([]byte(response))
	}))

	mux.HandleFunc("/dashboard-lcars/enqueue", with(rl, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Invalid form data</div>`))
			return
		}
		u := strings.TrimSpace(r.Form.Get("url"))
		if !validURL(u) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Invalid URL</div>`))
			return
		}

		// Enqueue immediately without metadata fetching to make it async
		id, err := mgr.Enqueue(u)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch err.Error() {
			case "queue_full":
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Queue is full, try again later</div>`))
			case "shutting_down":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Service is shutting down</div>`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Internal error</div>`))
			}
			return
		}

		// Create minimal DB record and fetch metadata asynchronously
		if storeCreate != nil {
			go func() {
				var dbid int64
				var title string
				var dur int64
				var thumb string
				
				// Create initial DB record with URL as title
				if idv, err := storeCreate(context.Background(), u, u, 0, "", "pending", 0); err == nil {
					dbid = idv
					mgr.AttachDB(id, dbid)
					
					// Fetch metadata asynchronously
					if mi, err := download.FetchMediaInfo(u); err == nil {
						title, dur, thumb = mi.Title, mi.DurationSec, mi.ThumbnailURL
						mgr.SetMeta(id, title, dur, thumb)
						if storeUpdateMeta != nil {
							_ = storeUpdateMeta(context.Background(), dbid, title, dur, thumb)
						}
					}
				} else {
					log.Printf("db create error: %v", err)
				}
			}()
		}

		// Return immediate success response with auto-clear and trigger queue refresh
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		
		// Generate response with success message and script to refresh queue
		response := `<div class="lcars-element left-rounded" style="background-color: #99cc99; color: black; padding: 8px;">✓ Video queued successfully <script>
			setTimeout(() => document.getElementById('enqueue-status').innerHTML = '', 3000);
			htmx.trigger('#queue', 'refresh');
		</script></div>`
		
		_, _ = w.Write([]byte(response))
	}))

	// Dashboard remove endpoint
	if st != nil {
		mux.HandleFunc("/dashboard/remove", with(rl, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			if err := r.ParseForm(); err != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Invalid form data</div>`))
				return
			}
			idStr := strings.TrimSpace(r.Form.Get("id"))
			var id int64
			if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Invalid ID</div>`))
				return
			}
			
			if err := st.DeleteDownload(r.Context(), id); err != nil {
				log.Printf("delete download id=%d: %v", id, err)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Failed to remove item</div>`))
				return
			}
			
			// Return success response and trigger queue refresh
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			response := `<div class="text-green-600 text-sm">✓ Item removed <script>
				setTimeout(() => document.getElementById('remove-status').innerHTML = '', 2000);
				htmx.trigger('#queue', 'refresh');
			</script></div>`
			_, _ = w.Write([]byte(response))
		}))

		mux.HandleFunc("/dashboard-lcars/remove", with(rl, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			if err := r.ParseForm(); err != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Invalid form data</div>`))
				return
			}
			idStr := strings.TrimSpace(r.Form.Get("id"))
			var id int64
			if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Invalid ID</div>`))
				return
			}
			
			if err := st.DeleteDownload(r.Context(), id); err != nil {
				log.Printf("delete download id=%d: %v", id, err)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="lcars-element left-rounded" style="background-color: #cc6677; color: white; padding: 8px;">Failed to remove item</div>`))
				return
			}
			
			// Return success response and trigger queue refresh
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			response := `<div class="lcars-element left-rounded" style="background-color: #99cc99; color: black; padding: 8px;">✓ Item removed <script>
				setTimeout(() => document.getElementById('remove-status').innerHTML = '', 2000);
				htmx.trigger('#queue', 'refresh');
			</script></div>`
			_, _ = w.Write([]byte(response))
		}))
	}

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
		// Skip noisy log line for HTMX row polling endpoint
		if r.URL.Path == "/dashboard/rows" {
			return
		}
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
	cap           int
	refill        time.Duration
	buckets       map[string]*bucket
	mu            sync.Mutex
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

type bucket struct {
	tokens int
	last   time.Time
}

func newIPRateLimiter(cap int, refill time.Duration) *ipRateLimiter {
	rl := &ipRateLimiter{
		cap:           cap,
		refill:        refill,
		buckets:       make(map[string]*bucket),
		cleanupTicker: time.NewTicker(time.Hour), // cleanup every hour
		stopCleanup:   make(chan struct{}),
	}
	// Start cleanup goroutine
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop removes stale buckets to prevent memory leaks
func (rl *ipRateLimiter) cleanupLoop() {
	for {
		select {
		case <-rl.cleanupTicker.C:
			rl.cleanup()
		case <-rl.stopCleanup:
			rl.cleanupTicker.Stop()
			return
		}
	}
}

// cleanup removes buckets that haven't been used for 24 hours
func (rl *ipRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-24 * time.Hour)
	for ip, bucket := range rl.buckets {
		if bucket.last.Before(cutoff) {
			delete(rl.buckets, ip)
		}
	}
}

// Stop stops the cleanup goroutine
func (rl *ipRateLimiter) Stop() {
	select {
	case <-rl.stopCleanup:
		// Already stopped
		return
	default:
		close(rl.stopCleanup)
	}
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
