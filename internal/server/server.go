package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

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
	PauseByDBID(dbID int64) bool
	CancelByDBID(dbID int64) bool
	ResumeByDBID(dbID int64) (bool, error)
}

type Options struct {
	UnsafeLogPayloads bool
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     isAllowedWebSocketOrigin,
}

const wsDiffCoalesceInterval = 200 * time.Millisecond

// New returns an http.Handler with routes and middleware wired.
// Minimal interface to abstract the store; nil store disables DB-backed features.
func New(mgr downloadManager, st *store.Store, outputDir string, opts ...Options) http.Handler {
	serverOpts := Options{}
	if len(opts) > 0 {
		serverOpts = opts[0]
	}

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
		// If store available, check for duplicates first.
		if st != nil {
			if existing, found, err := st.GetLatestDownloadByURL(r.Context(), req.URL); err == nil && found {
				writeJSON(w, http.StatusOK, map[string]any{
					"status":          "success",
					"message":         "already_exists",
					"existing_id":     existing.ID,
					"existing_status": existing.Status,
				})
				return
			}
		}

		// Create minimal DB record (async pattern - no blocking on metadata)
		var dbid int64
		if storeCreate != nil {
			// Fast insertion: store as pending with URL as title, no metadata fetching
			if idv, err := storeCreate(r.Context(), req.URL, req.URL, 0, "", "pending", 0); err == nil {
				dbid = idv
			} else {
				logging.LogDBOperation("create_download", 0, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
		} else {
			if _, err := mgr.Enqueue(req.URL); err != nil {
				msg := "internal_error"
				if err == download.ErrQueueFull {
					msg = "queue_full"
				} else if err == download.ErrShuttingDown {
					msg = "shutting_down"
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "message": msg})
				return
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
		createFailureCount := 0

		for _, u := range req.URLs {
			if !validURL(u) {
				continue
			}
			validURLCount++

			// If store available, check for duplicates and skip existing URLs.
			if st != nil {
				if _, found, err := st.GetLatestDownloadByURL(r.Context(), u); err == nil && found {
					duplicateCount++
					continue
				}
			}
			var dbid int64
			if storeCreate != nil {
				// Fast insertion: store as pending with URL as title, no metadata fetching
				if idv, err := storeCreate(r.Context(), u, u, 0, "", "pending", 0); err == nil {
					dbid = idv
					dbIDs = append(dbIDs, dbid)
				} else {
					logging.LogDBOperation("create_download", 0, err)
					createFailureCount++
				}
			}
		}

		statusCode, response := buildBatchResponse(validURLCount, dbIDs, duplicateCount, createFailureCount)
		writeJSON(w, statusCode, response)
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

		mux.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				methodNotAllowed(w)
				return
			}
			req, ok := parseControlRequest(w, r)
			if !ok {
				return
			}

			row, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if row.Status != "completed" {
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}

			if err := removeTrackedFiles(outputDir, row.ArtifactPaths); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "delete_failed"})
				return
			}
			if err := removeDownloadFile(outputDir, row.Filename); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "delete_failed"})
				return
			}

			if err := st.DeleteDownload(r.Context(), req.ID); err != nil {
				logging.LogDBOperation("delete_download", req.ID, err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "deleted", "id": req.ID})
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

			row, found, err := st.GetDownloadByID(r.Context(), id)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found || row.Filename == "" {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "file_not_found"})
				return
			}

			// Check if file exists in output directory
			filename := row.Filename
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
			f := parseListFilter(r.URL.Query())
			items, err := st.ListDownloads(r.Context(), f)
			if err != nil {
				slog.Error("failed to list downloads",
					"event", "list_downloads_error",
					"error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}

			// Log response for debugging; raw payload dump requires explicit unsafe opt-in.
			response := map[string]any{"status": "success", "downloads": items}
			if slog.Default().Enabled(r.Context(), slog.LevelDebug) {
				if serverOpts.UnsafeLogPayloads {
					if jsonBytes, err := json.Marshal(response); err == nil {
						slog.Debug("/api/downloads response (unsafe payload logging enabled)",
							"event", "api_response_raw",
							"endpoint", "/api/downloads",
							"data", string(jsonBytes))
					}
				} else {
					slog.Debug("/api/downloads response summary",
						"event", "api_response_summary",
						"endpoint", "/api/downloads",
						"download_count", len(items),
						"status_filter", f.Status,
						"sort", f.Sort,
						"order", f.Order)
				}
			}

			writeJSON(w, http.StatusOK, response)
		})

		mux.HandleFunc("/api/control/pause", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			req, ok := parseControlRequest(w, r)
			if !ok {
				return
			}

			row, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			switch row.Status {
			case "paused":
				writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_paused"})
				return
			case "completed", "canceled":
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}

			paused := mgr.PauseByDBID(req.ID)
			if !paused {
				pausedInDB, err := st.TryPause(r.Context(), req.ID)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				if !found {
					writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
					return
				}
				if pausedInDB {
					writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "paused", "download": updated})
					return
				}
				if updated.Status == "paused" {
					writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_paused", "download": updated})
					return
				}
				if updated.Status == "downloading" {
					if !mgr.PauseByDBID(req.ID) {
						writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
						return
					}
				} else {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
			}
			pausedInDB, err := st.TryPauseUnlessTerminal(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if !pausedInDB && (updated.Status == "completed" || updated.Status == "canceled") {
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "paused", "download": updated})
		})

		mux.HandleFunc("/api/control/resume", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			req, ok := parseControlRequest(w, r)
			if !ok {
				return
			}
			row, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if row.Status == "downloading" || row.Status == "pending" {
				writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_running"})
				return
			}
			if row.Status != "paused" && row.Status != "canceled" && row.Status != "error" {
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}

			if resumed, err := mgr.ResumeByDBID(req.ID); err != nil {
				msg := "internal_error"
				if err == download.ErrQueueFull {
					msg = "queue_full"
				} else if err == download.ErrShuttingDown {
					msg = "shutting_down"
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "message": msg})
				return
			} else if resumed {
				_, err := st.TryMarkResumed(r.Context(), req.ID)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				if !found {
					writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
					return
				}
				if updated.Status == "paused" || updated.Status == "canceled" {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "resumed", "download": updated})
				return
			}

			// Fallback: persist back to pending and let DB worker process it.
			if row.Status == "canceled" || row.Status == "error" {
				if _, err := st.TryMarkResumed(r.Context(), req.ID); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
			}
			if err := st.UpdateStatus(r.Context(), req.ID, "pending", ""); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "resumed", "download": updated})
		})

		mux.HandleFunc("/api/control/cancel", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			req, ok := parseControlRequest(w, r)
			if !ok {
				return
			}
			row, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if row.Status == "completed" {
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}
			if row.Status == "canceled" {
				writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_canceled"})
				return
			}

			cancelRequested := mgr.CancelByDBID(req.ID)
			if !cancelRequested {
				updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				if !found {
					writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
					return
				}
				if updated.Status == "canceled" {
					writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_canceled", "download": updated})
					return
				}
				if updated.Status == "completed" {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				if updated.Status == "downloading" {
					cancelRequested = mgr.CancelByDBID(req.ID)
					if !cancelRequested {
						writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
						return
					}
				}
			}

			var canceled bool
			if cancelRequested {
				canceled, err = st.TryCancel(r.Context(), req.ID)
			} else {
				canceled, err = st.TryCancelNotDownloading(r.Context(), req.ID)
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			updated, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if !canceled {
				if updated.Status == "downloading" && !cancelRequested {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				if updated.Status == "completed" {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				if updated.Status == "canceled" {
					writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "already_canceled", "download": updated})
					return
				}
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}
			if updated.Status != "canceled" {
				if updated.Status == "completed" {
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
				return
			}
			if cancelRequested {
				stableRow, stable, err := waitForStableCanceled(r.Context(), st, req.ID, 250*time.Millisecond)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
					return
				}
				if !stable {
					if stableRow.Status == "completed" {
						writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
						return
					}
					writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "invalid_state"})
					return
				}
				updated = stableRow
			}

			// For active cancellations, worker shutdown/cleanup is asynchronous; avoid
			// purging files here based on a timeout-window state sample.
			if !cancelRequested {
				_ = removeTrackedFiles(outputDir, updated.ArtifactPaths)
				if updated.Filename != "" {
					_ = removeDownloadFile(outputDir, updated.Filename)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "success", "message": "canceled", "download": updated})
		})

		mux.HandleFunc("/api/control/play", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			req, ok := parseControlRequest(w, r)
			if !ok {
				return
			}
			row, found, err := st.GetDownloadByID(r.Context(), req.ID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "internal_error"})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "not_found"})
				return
			}
			if row.Status != "completed" || strings.TrimSpace(row.Filename) == "" {
				writeJSON(w, http.StatusConflict, map[string]any{"status": "error", "message": "not_playable"})
				return
			}

			fullPath := filepath.Join(outputDir, row.Filename)
			if _, err := os.Stat(fullPath); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "file_not_found"})
				return
			}
			if err := openInDefaultPlayer(fullPath); err != nil {
				slog.Error("failed to launch default player", "event", "player_launch_error", "id", req.ID, "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "player_launch_failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "success",
				"message":  "play_started",
				"download": row,
			})
		})

		mux.HandleFunc("/api/ws/downloads", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			conn, err := wsUpgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			f := parseListFilter(r.URL.Query())
			writeSnapshot := func() ([]store.Download, error) {
				rows, err := st.ListDownloads(r.Context(), f)
				if err != nil {
					return nil, err
				}
				if err := conn.WriteJSON(map[string]any{
					"type":      "snapshot",
					"downloads": rows,
					"at":        time.Now().UTC().Format(time.RFC3339Nano),
				}); err != nil {
					return nil, err
				}
				return rows, nil
			}
			changes, unsubscribe := st.SubscribeChanges(512)
			defer unsubscribe()
			prevRows, err := writeSnapshot()
			if err != nil {
				return
			}

			prevByID := mapDownloadsByID(prevRows)
			coalesceTicker := time.NewTicker(wsDiffCoalesceInterval)
			defer coalesceTicker.Stop()
			heartbeatTicker := time.NewTicker(15 * time.Second)
			defer heartbeatTicker.Stop()
			pendingDiff := false
			for {
				select {
				case <-r.Context().Done():
					return
				case _, ok := <-changes:
					if !ok {
						return
					}
					pendingDiff = true
				case <-coalesceTicker.C:
					if !pendingDiff {
						continue
					}
					rows, err := st.ListDownloads(r.Context(), f)
					if err != nil {
						return
					}
					currentByID := mapDownloadsByID(rows)
					diff := buildDownloadsDiff(prevByID, currentByID)
					prevByID = currentByID
					pendingDiff = false
					if len(diff.Upserts) > 0 || len(diff.Deletes) > 0 {
						if err := conn.WriteJSON(map[string]any{
							"type":    "diff",
							"upserts": diff.Upserts,
							"deletes": diff.Deletes,
							"at":      time.Now().UTC().Format(time.RFC3339Nano),
						}); err != nil {
							return
						}
					}
				case <-heartbeatTicker.C:
					if err := conn.WriteJSON(map[string]any{
						"type": "heartbeat",
						"at":   time.Now().UTC().Format(time.RFC3339Nano),
					}); err != nil {
						return
					}
				}
			}
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
				case "paused":
					stt = download.StatePaused
				case "canceled":
					stt = download.StateCanceled
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
					Error:        d.ErrorMessage,
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
			if _, found, err := st.GetLatestDownloadByURL(r.Context(), u); err == nil && found {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				response := `<div class="text-blue-600 text-sm">✓ Video already exists <script>
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
			if _, err := storeCreate(r.Context(), u, u, 0, "", "pending", 0); err != nil {
				logging.LogDBOperation("create_download", 0, err)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<div class="text-red-600 text-sm">Failed to queue video</div>`))
				return
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

type controlRequest struct {
	ID int64 `json:"id"`
}

func parseControlRequest(w http.ResponseWriter, r *http.Request) (controlRequest, bool) {
	var req controlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid_request"})
		return controlRequest{}, false
	}
	return req, true
}

func parseListFilter(q url.Values) store.ListFilter {
	f := store.ListFilter{
		Status: q.Get("status"),
		Sort:   q.Get("sort"),
		Order:  q.Get("order"),
	}
	if lim := strings.TrimSpace(q.Get("limit")); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil && n > 0 {
			f.Limit = n
		}
	}
	if off := strings.TrimSpace(q.Get("offset")); off != "" {
		if n, err := strconv.Atoi(off); err == nil && n >= 0 {
			f.Offset = n
		}
	}
	return f
}

type downloadsDiff struct {
	Upserts []store.Download
	Deletes []int64
}

func mapDownloadsByID(rows []store.Download) map[int64]store.Download {
	out := make(map[int64]store.Download, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func buildDownloadsDiff(prev, current map[int64]store.Download) downloadsDiff {
	diff := downloadsDiff{
		Upserts: make([]store.Download, 0),
		Deletes: make([]int64, 0),
	}

	for id, row := range current {
		prevRow, ok := prev[id]
		if !ok || !downloadsEqual(prevRow, row) {
			diff.Upserts = append(diff.Upserts, row)
		}
	}
	for id := range prev {
		if _, ok := current[id]; !ok {
			diff.Deletes = append(diff.Deletes, id)
		}
	}

	sort.Slice(diff.Upserts, func(i, j int) bool {
		return diff.Upserts[i].ID < diff.Upserts[j].ID
	})
	sort.Slice(diff.Deletes, func(i, j int) bool {
		return diff.Deletes[i] < diff.Deletes[j]
	})
	return diff
}

func downloadsEqual(a, b store.Download) bool {
	if a.ID != b.ID ||
		a.URL != b.URL ||
		a.Title != b.Title ||
		a.Duration != b.Duration ||
		a.ThumbnailURL != b.ThumbnailURL ||
		a.Status != b.Status ||
		a.Progress != b.Progress ||
		a.Filename != b.Filename ||
		a.ErrorMessage != b.ErrorMessage ||
		!a.CreatedAt.Equal(b.CreatedAt) ||
		!a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	if len(a.ArtifactPaths) != len(b.ArtifactPaths) {
		return false
	}
	for i := range a.ArtifactPaths {
		if a.ArtifactPaths[i] != b.ArtifactPaths[i] {
			return false
		}
	}
	return true
}

func buildBatchResponse(validURLCount int, dbIDs []int64, duplicateCount int, createFailureCount int) (int, map[string]any) {
	if validURLCount == 0 {
		return http.StatusBadRequest, map[string]any{"status": "error", "message": "no_valid_urls"}
	}

	if len(dbIDs) == 0 && duplicateCount > 0 && createFailureCount == 0 {
		return http.StatusOK, map[string]any{"status": "success", "message": "all_already_exists", "duplicates": duplicateCount}
	}

	if len(dbIDs) == 0 && createFailureCount > 0 {
		resp := map[string]any{
			"status":  "error",
			"message": "internal_error",
			"failed":  createFailureCount,
		}
		if duplicateCount > 0 {
			resp["duplicates_skipped"] = duplicateCount
		}
		return http.StatusInternalServerError, resp
	}

	response := map[string]any{"status": "success", "message": "enqueued", "db_ids": dbIDs}
	if duplicateCount > 0 {
		response["duplicates_skipped"] = duplicateCount
	}
	if createFailureCount > 0 {
		response["failed"] = createFailureCount
		response["message"] = "partial_enqueued"
	}
	return http.StatusOK, response
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

func isAllowedWebSocketOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "chrome-extension":
		return parsed.Host != ""
	case "http", "https":
		return strings.EqualFold(parsed.Host, r.Host)
	default:
		return false
	}
}

func removeDownloadFile(outputDir, filename string) error {
	if strings.TrimSpace(filename) == "" {
		return nil
	}
	fullPath := filepath.Join(outputDir, filename)
	if _, err := os.Stat(fullPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Remove(fullPath)
}

func removeTrackedFiles(outputDir string, tracked []string) error {
	if len(tracked) == 0 {
		return nil
	}

	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(tracked))
	for _, path := range tracked {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		full := trimmed
		if !filepath.IsAbs(full) {
			full = filepath.Join(outputDir, full)
		}
		full, err = filepath.Abs(full)
		if err != nil {
			continue
		}
		if !isPathWithin(absOutputDir, full) {
			continue
		}
		seen[full] = struct{}{}
	}

	for file := range seen {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func isPathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func waitForStableCanceled(ctx context.Context, st *store.Store, id int64, window time.Duration) (store.Download, bool, error) {
	deadline := time.Now().Add(window)
	var last store.Download

	for {
		row, found, err := st.GetDownloadByID(ctx, id)
		if err != nil {
			return store.Download{}, false, err
		}
		if !found {
			return store.Download{}, false, nil
		}
		last = row
		if row.Status != "canceled" {
			return row, false, nil
		}
		if time.Now().After(deadline) {
			return row, true, nil
		}

		select {
		case <-ctx.Done():
			return last, false, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
}

func openInDefaultPlayer(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("default player exited with error", "event", "player_exit_error", "path", path, "error", err)
		}
	}()
	return nil
}

// Middleware

type responseRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, opts *http.PushOptions) error {
	pusher, ok := r.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := newResponseRecorder(w)
		next.ServeHTTP(rec, r)
		// Skip noisy log line for HTMX row polling endpoint
		if r.URL.Path == "/dashboard/rows" {
			return
		}
		logging.LogHTTPRequest(r.Method, r.URL.Path, r.RemoteAddr, time.Since(start), rec.statusCode, rec.bytesWritten)
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
