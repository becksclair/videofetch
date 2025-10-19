package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"videofetch/internal/download"
	"videofetch/internal/logging"
	"videofetch/internal/store"
	"videofetch/internal/ui"
)

type downloadManager interface {
	Enqueue(url string) (string, error)
	Snapshot(id string) []*download.Item
	AttachDB(id string, dbID int64)
	SetMeta(id string, title string, duration int64, thumb string)
}

// New returns an http.Handler with routes and middleware wired.
// Minimal interface to abstract the store; nil store disables DB-backed features.
func New(mgr downloadManager, st *store.Store, outputDir string) http.Handler {
	mux := http.NewServeMux()
	// helpers
	var storeCreate func(ctx context.Context, url, title string, duration int64, thumbnail string, status string, progress float64) (int64, error)
	if st != nil {
		storeCreate = st.CreateDownload
	}

	// Routes
	mux.HandleFunc("/api/download_single", func(w http.ResponseWriter, r *http.Request) {
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
		// If store available, check for duplicates first
		if st != nil {
			if completed, err := st.IsURLCompleted(r.Context(), req.URL); err == nil && completed {
				// URL already completed, silently return success without enqueueing
				writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_completed"})
				return
			}
		}

		// Create minimal DB record (async pattern - no blocking on metadata)
		var dbid int64
		if storeCreate != nil {
			// Fast insertion: store as pending with URL as title, no metadata fetching
			if idv, err := storeCreate(r.Context(), req.URL, req.URL, 0, "", "pending", 0); err == nil {
				dbid = idv

				// Process download asynchronously (fetches metadata, then enqueues)
				if manager, ok := mgr.(*download.Manager); ok && st != nil {
					go func(dbID int64, url string) {
						ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
						defer cancel()
						if err := manager.ProcessPendingDownload(ctx, dbID, url, st); err != nil {
							slog.Error("ProcessPendingDownload failed",
								"event", "process_download_error",
								"db_id", dbID,
								"url", url,
								"error", err)
						}
					}(dbid, req.URL)
				}
			} else {
				logging.LogDBOperation("create_download", 0, err)
			}
		}

		// Return success immediately (async-first)
		if dbid > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued", "db_id": dbid})
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "enqueued"})
		}
	})

	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
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
		dbIDs := make([]int64, 0, len(req.URLs))
		validURLCount := 0
		duplicateCount := 0

		for _, u := range req.URLs {
			if !validURL(u) {
				continue
			}
			validURLCount++

			// If store available, check for duplicates and skip completed URLs
			if st != nil {
				if completed, err := st.IsURLCompleted(r.Context(), u); err == nil && completed {
					duplicateCount++
					continue // Skip already completed URLs silently
				}
			}
			var dbid int64
			if storeCreate != nil {
				// Fast insertion: store as pending with URL as title, no metadata fetching
				if idv, err := storeCreate(r.Context(), u, u, 0, "", "pending", 0); err == nil {
					dbid = idv
					dbIDs = append(dbIDs, dbid)

					// Immediately process in background goroutine instead of waiting for DBWorker
					if manager, ok := mgr.(*download.Manager); ok && st != nil {
						go func(dbID int64, url string) {
							ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
							defer cancel()
							if err := manager.ProcessPendingDownload(ctx, dbID, url, st); err != nil {
								slog.Error("ProcessPendingDownload failed",
									"event", "process_download_error",
									"db_id", dbID,
									"url", url,
									"error", err)
							}
						}(dbid, u)
					}
				} else {
					logging.LogDBOperation("create_download", 0, err)
				}
			}
		}

		if validURLCount == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "no_valid_urls"})
			return
		}

		if len(dbIDs) == 0 && duplicateCount > 0 {
			// All valid URLs were duplicates - return success
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "all_already_completed", "duplicates": duplicateCount})
			return
		}

		response := map[string]any{"status": "success", "message": "enqueued", "db_ids": dbIDs}
		if duplicateCount > 0 {
			response["duplicates_skipped"] = duplicateCount
		}
		writeJSON(w, http.StatusOK, response)
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		id := r.URL.Query().Get("id")
		items := mgr.Snapshot(id)
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "downloads": items})
	})

	// Optional DB-backed listing; only registered if store is provided via main.
	if st != nil {
		mux.HandleFunc("/api/retry_failed", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			affected, err := st.RetryFailedDownloads(r.Context())
			if err != nil {
				logging.LogRetryFailed(0, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "retried", "count": affected})
		})

		mux.HandleFunc("/api/remove", func(w http.ResponseWriter, r *http.Request) {
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
				logging.LogDBOperation("delete_download", req.ID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "deleted"})
		})

		mux.HandleFunc("/api/download_file", func(w http.ResponseWriter, r *http.Request) {
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
				slog.Error("failed to stat file",
					"event", "file_stat_error",
					"path", fullPath,
					"error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}

			// Serve the file
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			http.ServeFile(w, r, fullPath)
		})

		mux.HandleFunc("/api/downloads", func(w http.ResponseWriter, r *http.Request) {
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
				slog.Error("failed to list downloads",
					"event", "list_downloads_error",
					"error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}

			// Log the JSON response for debugging (only in debug level)
			response := map[string]any{"status": "success", "downloads": items}
			if slog.Default().Enabled(r.Context(), slog.LevelDebug) {
				if jsonBytes, err := json.Marshal(response); err == nil {
					slog.Debug("/api/downloads response",
						"event", "api_response",
						"endpoint", "/api/downloads",
						"data", string(jsonBytes))
				}
			}

			writeJSON(w, http.StatusOK, response)
		})
	}

	// Dashboard (HTML via Templ + HTMX)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	})

	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		items := mgr.Snapshot("")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.Dashboard(items).Render(context.Background(), w)
	})

	mux.HandleFunc("/dashboard/rows", func(w http.ResponseWriter, r *http.Request) {
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
				slog.Error("failed to list downloads for dashboard",
					"event", "list_downloads_error",
					"endpoint", "/dashboard/rows",
					"error", err)
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
	})

	mux.HandleFunc("/dashboard/enqueue", func(w http.ResponseWriter, r *http.Request) {
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

		// Check for duplicates first (before any DB write)
		if st != nil {
			if completed, err := st.IsURLCompleted(r.Context(), u); err == nil && completed {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				response := `<div class="text-blue-600 text-sm">✓ Video already downloaded <script>
					setTimeout(() => document.getElementById('enqueue-status').innerHTML = '', 3000);
					htmx.trigger('#queue', 'refresh');
				</script></div>`
				_, _ = w.Write([]byte(response))
				return
			}
		}

		// Create minimal DB record (async pattern - no blocking on metadata)
		if storeCreate != nil {
			// Fast insertion: store as pending with URL as title, no metadata fetching
			if dbid, err := storeCreate(r.Context(), u, u, 0, "", "pending", 0); err == nil {
				// Process download asynchronously (fetches metadata, then enqueues)
				if manager, ok := mgr.(*download.Manager); ok && st != nil {
					go func(dbID int64, url string) {
						ctx := context.Background()
						if err := manager.ProcessPendingDownload(ctx, dbID, url, st); err != nil {
							slog.Error("ProcessPendingDownload failed",
								"event", "process_download_error",
								"db_id", dbID,
								"url", url,
								"error", err)
						}
					}(dbid, u)
				}
			} else {
				logging.LogDBOperation("create_download", 0, err)
			}
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
	})

	// Dashboard remove endpoint
	if st != nil {
		mux.HandleFunc("/dashboard/remove", func(w http.ResponseWriter, r *http.Request) {
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
				logging.LogDBOperation("delete_download", id, err)
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
		})

		// Dashboard retry failed endpoint
		mux.HandleFunc("/dashboard/retry_failed", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			affected, err := st.RetryFailedDownloads(r.Context())
			if err != nil {
				logging.LogRetryFailed(0, err)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Failed to retry downloads</div>`))
				return
			}
			// Return success response and trigger queue refresh
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			response := fmt.Sprintf(`<div class="text-green-600 text-sm">✓ Retried %d failed downloads <script>
				setTimeout(() => document.getElementById('retry-status').innerHTML = '', 3000);
				htmx.trigger('#queue', 'refresh');
			</script></div>`, affected)
			_, _ = w.Write([]byte(response))
		})
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

func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// Skip noisy log line for HTMX row polling endpoint
		if r.URL.Path == "/dashboard/rows" {
			return
		}
		// TODO: capture response status code properly with a response writer wrapper
		logging.LogHTTPRequest(r.Method, r.URL.Path, r.RemoteAddr, time.Since(start), 200)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic recovered",
					"event", "panic_recovered",
					"path", r.URL.Path,
					"method", r.Method,
					"panic", v)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
