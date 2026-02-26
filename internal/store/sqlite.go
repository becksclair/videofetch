package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"videofetch/internal/logging"

	_ "modernc.org/sqlite"
)

// Download represents a row in the downloads table.
type Download struct {
	ID            int64     `json:"id"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Duration      int64     `json:"duration"` // seconds
	ThumbnailURL  string    `json:"thumbnail_url"`
	Status        string    `json:"status"`
	Progress      float64   `json:"progress"`
	Filename      string    `json:"filename"`
	ArtifactPaths []string  `json:"artifact_paths,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Implement IncompleteDownload interface for Download
func (d *Download) GetID() int64            { return d.ID }
func (d *Download) GetURL() string          { return d.URL }
func (d *Download) GetTitle() string        { return d.Title }
func (d *Download) GetDuration() int64      { return d.Duration }
func (d *Download) GetThumbnailURL() string { return d.ThumbnailURL }
func (d *Download) GetStatus() string       { return d.Status }
func (d *Download) GetProgress() float64    { return d.Progress }

// Store wraps an sql.DB and provides typed helpers.
type Store struct {
	db *sql.DB

	subMu sync.RWMutex
	subs  map[chan ChangeEvent]struct{}
}

type ChangeType string

const (
	ChangeUpsert ChangeType = "upsert"
	ChangeDelete ChangeType = "delete"
)

type ChangeEvent struct {
	Type ChangeType
	ID   int64 // 0 means "resync needed"
}

// Open opens or creates a SQLite database at the given path and ensures schema.
func Open(path string) (*Store, error) {
	// Pragmas: busy timeout and WAL for better concurrency.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_journal_mode=WAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Conservative limits.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:   db,
		subs: make(map[chan ChangeEvent]struct{}),
	}, nil
}

func initSchema(db *sql.DB) error {
	// Create table if not exists.
	const ddl = `
CREATE TABLE IF NOT EXISTS downloads (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL,
    title TEXT,
    duration INTEGER,
    thumbnail_url TEXT,
    status TEXT,
    progress REAL,
    filename TEXT,
    artifact_paths TEXT,
    error_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_downloads_status ON downloads(status);
CREATE INDEX IF NOT EXISTS idx_downloads_created_at ON downloads(created_at);
CREATE INDEX IF NOT EXISTS idx_downloads_url_status ON downloads(url, status);
`
	_, err := db.Exec(ddl)
	if err != nil {
		return err
	}

	if err := ensureColumn(db, "downloads", "filename", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(db, "downloads", "artifact_paths", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(db, "downloads", "error_message", "TEXT"); err != nil {
		return err
	}

	return nil
}

func ensureColumn(db *sql.DB, table, column, colType string) error {
	hasCol, err := hasColumn(db, table, column)
	if err != nil {
		return err
	}
	if hasCol {
		return nil
	}

	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, colType))
	return err
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close closes the underlying DB.
func (s *Store) Close() error { return s.db.Close() }

// SubscribeChanges subscribes to mutation events.
// The returned unsubscribe function must be called to avoid leaks.
func (s *Store) SubscribeChanges(buffer int) (<-chan ChangeEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan ChangeEvent, buffer)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()

	unsubscribe := func() {
		s.subMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
		}
		s.subMu.Unlock()
	}
	return ch, unsubscribe
}

func (s *Store) emitChange(evt ChangeEvent) {
	s.subMu.RLock()
	targets := make([]chan ChangeEvent, 0, len(s.subs))
	for ch := range s.subs {
		targets = append(targets, ch)
	}
	s.subMu.RUnlock()

	for _, ch := range targets {
		select {
		case ch <- evt:
		default:
			// Channel is saturated; collapse to a single resync event.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ChangeEvent{Type: ChangeUpsert, ID: 0}:
			default:
			}
		}
	}
}

// CreateDownload inserts a new download row and returns its ID.
func (s *Store) CreateDownload(ctx context.Context, url, title string, duration int64, thumbnail string, status string, progress float64) (int64, error) {
	if url == "" {
		return 0, ErrEmptyURL
	}
	// normalize status
	st := normalizeStatus(status)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO downloads (url, title, duration, thumbnail_url, status, progress, artifact_paths)
VALUES (?, ?, ?, ?, ?, ?, ?)`, url, title, duration, thumbnail, st, progress, "[]")
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get insert id: %w", err)
	}
	logging.LogDBCreate(id, url, title, int(duration), st, progress)
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return id, nil
}

// UpdateArtifacts stores tracked artifact paths for deterministic cleanup.
func (s *Store) UpdateArtifacts(ctx context.Context, id int64, paths []string) error {
	cleaned := cleanArtifactPaths(paths)
	payload, err := json.Marshal(cleaned)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE downloads SET artifact_paths = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(payload), id)
	if err != nil {
		return err
	}
	logging.LogDBUpdate("update_artifacts", id, map[string]any{"artifact_count": len(cleaned)})
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return nil
}

// UpdateProgress sets progress and bumps updated_at.
func (s *Store) UpdateProgress(ctx context.Context, id int64, progress float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET progress = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, progress, id)
	if err != nil {
		return err
	}
	logging.LogDBUpdate("update_progress", id, map[string]any{"progress": progress})
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return nil
}

// UpdateStatus sets status (and optionally title/thumbnail if provided) and bumps updated_at.
func (s *Store) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
	st := normalizeStatus(status)
	var err error
	if st == "error" {
		trimmedErr := strings.TrimSpace(errMsg)
		if trimmedErr == "" {
			_, err = s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, st, id)
		} else {
			_, err = s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, st, trimmedErr, id)
		}
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, st, id)
	}
	if err != nil {
		return err
	}
	fields := map[string]any{"status": st}
	if errMsg != "" {
		fields["error_message"] = errMsg
	}
	logging.LogDBUpdate("update_status", id, fields)
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return nil
}

// UpdateMeta updates title/thumbnail/duration if non-zero values are provided.
func (s *Store) UpdateMeta(ctx context.Context, id int64, title string, duration int64, thumbnail string) error {
	// Build dynamic set clause for provided fields.
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if title != "" {
		sets = append(sets, "title = ?")
		args = append(args, title)
	}
	if duration > 0 {
		sets = append(sets, "duration = ?")
		args = append(args, duration)
	}
	if thumbnail != "" {
		sets = append(sets, "thumbnail_url = ?")
		args = append(args, thumbnail)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	q := "UPDATE downloads SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	// Only log fields that were updated
	fields := map[string]any{"title": title, "duration": duration}
	if thumbnail != "" {
		fields["thumbnail_set"] = true
	}
	logging.LogDBUpdate("update_meta", id, fields)
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return nil
}

// TryClaimPending atomically transitions a pending download to downloading.
// Returns true when claim succeeds, false when the row was not pending.
func (s *Store) TryClaimPending(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'downloading', error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// TryCancel transitions a download to canceled unless it is already completed/canceled.
// Returns true when the transition was applied.
func (s *Store) TryCancel(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'canceled', error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status NOT IN ('completed', 'canceled')`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		logging.LogDBUpdate("try_cancel", id, map[string]any{"status": "canceled"})
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// TryCancelNotDownloading transitions a download to canceled only when it is not downloading.
// Returns true when the transition was applied.
func (s *Store) TryCancelNotDownloading(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'canceled', error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status NOT IN ('completed', 'canceled', 'downloading')`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		logging.LogDBUpdate("try_cancel_not_downloading", id, map[string]any{"status": "canceled"})
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// TryPause transitions a download to paused only when it is not downloading/completed/canceled.
// Returns true when the transition was applied.
func (s *Store) TryPause(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'paused', error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status IN ('pending', 'error')`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		logging.LogDBUpdate("try_pause", id, map[string]any{"status": "paused"})
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// TryPauseUnlessTerminal transitions a download to paused unless it is in a terminal state.
// Returns true when the transition was applied.
func (s *Store) TryPauseUnlessTerminal(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'paused', error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status NOT IN ('completed', 'canceled')`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		logging.LogDBUpdate("try_pause_unless_terminal", id, map[string]any{"status": "paused"})
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// TryMarkResumed transitions a paused/canceled/error download back to downloading.
// Returns true when the transition was applied.
func (s *Store) TryMarkResumed(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE downloads
SET status = 'downloading',
    error_message = NULL,
    progress = CASE WHEN status IN ('canceled', 'error') THEN 0 ELSE progress END,
    filename = CASE WHEN status IN ('canceled', 'error') THEN NULL ELSE filename END,
    artifact_paths = CASE WHEN status IN ('canceled', 'error') THEN NULL ELSE artifact_paths END,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('paused', 'canceled', 'error')`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		logging.LogDBUpdate("try_mark_resumed", id, map[string]any{"status": "downloading"})
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	}
	return affected == 1, nil
}

// ListDownloads returns downloads filtered and sorted.
type ListFilter struct {
	Status string // optional: pending|downloading|paused|completed|error|canceled
	Sort   string // created_at|title|status
	Order  string // asc|desc
	Limit  int    // optional
	Offset int    // optional
}

func (s *Store) ListDownloads(ctx context.Context, f ListFilter) ([]Download, error) {
	sortCol := "created_at"
	switch strings.ToLower(f.Sort) {
	case "title":
		sortCol = "title"
	case "status":
		sortCol = "status"
	case "created_at", "date":
		sortCol = "created_at"
	}
	order := "DESC"
	if strings.ToLower(f.Order) == "asc" {
		order = "ASC"
	}
	var args []any
	sb := strings.Builder{}
	sb.WriteString("SELECT id, url, title, duration, thumbnail_url, status, progress, filename, artifact_paths, error_message, created_at, updated_at FROM downloads")
	if f.Status != "" {
		sb.WriteString(" WHERE status = ?")
		args = append(args, normalizeStatus(f.Status))
	}
	sb.WriteString(" ORDER BY ")
	sb.WriteString(sortCol)
	sb.WriteByte(' ')
	sb.WriteString(order)
	if f.Limit > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, f.Limit)
		if f.Offset > 0 {
			sb.WriteString(" OFFSET ?")
			args = append(args, f.Offset)
		}
	} else if f.Offset > 0 {
		sb.WriteString(" LIMIT -1 OFFSET ?")
		args = append(args, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Download, 0, 64)
	for rows.Next() {
		var d Download
		var filename sql.NullString
		var artifactPaths sql.NullString
		var errorMessage sql.NullString
		if err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, &d.Status, &d.Progress, &filename, &artifactPaths, &errorMessage, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Filename = filename.String
		d.ArtifactPaths = parseArtifactPaths(artifactPaths.String)
		d.ErrorMessage = errorMessage.String
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDownloadByID returns a single download by ID.
func (s *Store) GetDownloadByID(ctx context.Context, id int64) (Download, bool, error) {
	var d Download
	var filename sql.NullString
	var artifactPaths sql.NullString
	var errorMessage sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, url, title, duration, thumbnail_url, status, progress, filename, artifact_paths, error_message, created_at, updated_at
FROM downloads
WHERE id = ?`, id).Scan(
		&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, &d.Status, &d.Progress, &filename, &artifactPaths, &errorMessage, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, false, nil
	}
	if err != nil {
		return Download{}, false, err
	}
	d.Filename = filename.String
	d.ArtifactPaths = parseArtifactPaths(artifactPaths.String)
	d.ErrorMessage = errorMessage.String
	return d, true, nil
}

// UpdateFilename sets the filename when download is complete.
func (s *Store) UpdateFilename(ctx context.Context, id int64, filename string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET filename = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, filename, id)
	if err != nil {
		return err
	}
	logging.LogDBUpdate("update_filename", id, map[string]any{"filename": filename})
	s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: id})
	return nil
}

// DeleteDownload removes a download record from the database.
func (s *Store) DeleteDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id = ?`, id)
	if err != nil {
		return err
	}
	logging.LogDBOperation("delete_download", id, nil)
	s.emitChange(ChangeEvent{Type: ChangeDelete, ID: id})
	return nil
}

// IsURLCompleted checks if a URL already exists with status "completed"
func (s *Store) IsURLCompleted(ctx context.Context, url string) (bool, error) {
	if url == "" {
		return false, ErrEmptyURL
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE url = ? AND status = 'completed'`, url).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetLatestDownloadByURL returns the most recently updated record for a URL.
func (s *Store) GetLatestDownloadByURL(ctx context.Context, inputURL string) (Download, bool, error) {
	if strings.TrimSpace(inputURL) == "" {
		return Download{}, false, ErrEmptyURL
	}

	var d Download
	var filename sql.NullString
	var artifactPaths sql.NullString
	var errorMessage sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, url, title, duration, thumbnail_url, status, progress, filename, artifact_paths, error_message, created_at, updated_at
FROM downloads
WHERE url = ?
ORDER BY updated_at DESC, id DESC
LIMIT 1`, inputURL).Scan(
		&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, &d.Status, &d.Progress, &filename, &artifactPaths, &errorMessage, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, false, nil
	}
	if err != nil {
		return Download{}, false, err
	}
	d.Filename = filename.String
	d.ArtifactPaths = parseArtifactPaths(artifactPaths.String)
	d.ErrorMessage = errorMessage.String
	return d, true, nil
}

// GetPendingDownloads returns downloads with "pending" status, ordered by creation time
func (s *Store) GetPendingDownloads(ctx context.Context, limit int) ([]Download, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT id, url, title, duration, thumbnail_url, status, progress, filename, artifact_paths, error_message, created_at, updated_at
			  FROM downloads 
			  WHERE status = 'pending' 
			  ORDER BY created_at ASC 
			  LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var downloads []Download
	for rows.Next() {
		var d Download
		var filename sql.NullString
		var artifactPaths sql.NullString
		var errorMessage sql.NullString
		err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL,
			&d.Status, &d.Progress, &filename, &artifactPaths, &errorMessage, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if filename.Valid {
			d.Filename = filename.String
		}
		if artifactPaths.Valid {
			d.ArtifactPaths = parseArtifactPaths(artifactPaths.String)
		}
		if errorMessage.Valid {
			d.ErrorMessage = errorMessage.String
		}
		downloads = append(downloads, d)
	}
	return downloads, rows.Err()
}

// GetIncompleteDownloads returns startup-retriable downloads.
// Paused and canceled rows are intentionally excluded because they are terminal
// until explicit user action.
func (s *Store) GetIncompleteDownloads(ctx context.Context, limit int) ([]interface {
	GetID() int64
	GetURL() string
	GetTitle() string
	GetDuration() int64
	GetThumbnailURL() string
	GetStatus() string
	GetProgress() float64
}, error) {
	if limit <= 0 {
		limit = 50 // reasonable default for startup retry
	}
	query := `SELECT id, url, title, duration, thumbnail_url, status, progress, filename, artifact_paths, error_message, created_at, updated_at
			  FROM downloads
			  WHERE status IN ('pending', 'downloading', 'error')
			  ORDER BY created_at ASC
			  LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var downloads []interface {
		GetID() int64
		GetURL() string
		GetTitle() string
		GetDuration() int64
		GetThumbnailURL() string
		GetStatus() string
		GetProgress() float64
	}
	for rows.Next() {
		var d Download
		var filename sql.NullString
		var artifactPaths sql.NullString
		var errorMessage sql.NullString
		err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL,
			&d.Status, &d.Progress, &filename, &artifactPaths, &errorMessage, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if filename.Valid {
			d.Filename = filename.String
		}
		if artifactPaths.Valid {
			d.ArtifactPaths = parseArtifactPaths(artifactPaths.String)
		}
		if errorMessage.Valid {
			d.ErrorMessage = errorMessage.String
		}
		downloads = append(downloads, &d)
	}
	return downloads, rows.Err()
}

// GetPendingDownloadsForWorker returns downloads with "pending" status in a format suitable for the download worker
func (s *Store) GetPendingDownloadsForWorker(ctx context.Context, limit int) ([]interface{}, error) {
	downloads, err := s.GetPendingDownloads(ctx, limit)
	if err != nil {
		return nil, err
	}

	// Convert to interface{} slice of maps for the dbworker
	result := make([]interface{}, len(downloads))
	for i, d := range downloads {
		result[i] = map[string]interface{}{
			"id":            d.ID,
			"url":           d.URL,
			"title":         d.Title,
			"duration":      d.Duration,
			"thumbnail_url": d.ThumbnailURL,
			"status":        d.Status,
		}
	}
	return result, nil
}

// CountDownloadsByStatus returns the count of downloads by status
func (s *Store) CountDownloadsByStatus(ctx context.Context, status string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE status = ?`, normalizeStatus(status)).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// RetryFailedDownloads resets all failed downloads back to pending status for retry
func (s *Store) RetryFailedDownloads(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = 'pending', progress = 0, error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE status = 'error'`)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	logging.LogRetryFailed(affected, nil)
	if affected > 0 {
		s.emitChange(ChangeEvent{Type: ChangeUpsert, ID: 0})
	}
	return affected, nil
}

func normalizeStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "queued":
		return "pending"
	case "downloading", "completed", "pending", "paused", "canceled":
		return s
	case "failed", "error":
		return "error"
	default:
		return "pending"
	}
}

func parseArtifactPaths(input string) []string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	return cleanArtifactPaths(parsed)
}

func cleanArtifactPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		p := strings.TrimSpace(path)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
