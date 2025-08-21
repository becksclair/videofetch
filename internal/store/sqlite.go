package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Download represents a row in the downloads table.
type Download struct {
	ID           int64     `json:"id"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Duration     int64     `json:"duration"` // seconds
	ThumbnailURL string    `json:"thumbnail_url"`
	Status       string    `json:"status"`
	Progress     float64   `json:"progress"`
	Filename     string    `json:"filename"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Implement IncompleteDownload interface for Download
func (d *Download) GetID() int64           { return d.ID }
func (d *Download) GetURL() string         { return d.URL }
func (d *Download) GetTitle() string       { return d.Title }
func (d *Download) GetDuration() int64     { return d.Duration }
func (d *Download) GetThumbnailURL() string { return d.ThumbnailURL }
func (d *Download) GetStatus() string      { return d.Status }
func (d *Download) GetProgress() float64   { return d.Progress }

// Store wraps an sql.DB and provides typed helpers.
type Store struct {
	db *sql.DB
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
	return &Store{db: db}, nil
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

	// Add filename column if it doesn't exist (migration for existing DBs)
	_, err = db.Exec(`ALTER TABLE downloads ADD COLUMN filename TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	return nil
}

// Close closes the underlying DB.
func (s *Store) Close() error { return s.db.Close() }

// CreateDownload inserts a new download row and returns its ID.
func (s *Store) CreateDownload(ctx context.Context, url, title string, duration int64, thumbnail string, status string, progress float64) (int64, error) {
	if url == "" {
		return 0, errors.New("empty_url")
	}
	// normalize status
	st := normalizeStatus(status)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO downloads (url, title, duration, thumbnail_url, status, progress)
VALUES (?, ?, ?, ?, ?, ?)`, url, title, duration, thumbnail, st, progress)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get insert id: %w", err)
	}
	log.Printf("db: create_download id=%d url=%q title=%q duration=%d status=%s progress=%.1f", id, url, title, duration, st, progress)
	return id, nil
}

// UpdateProgress sets progress and bumps updated_at.
func (s *Store) UpdateProgress(ctx context.Context, id int64, progress float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET progress = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, progress, id)
	if err != nil {
		return err
	}
	log.Printf("db: update_progress id=%d progress=%.1f", id, progress)
	return nil
}

// UpdateStatus sets status (and optionally title/thumbnail if provided) and bumps updated_at.
func (s *Store) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
	st := normalizeStatus(status)
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, st, id)
	if err != nil {
		return err
	}
	if errMsg != "" {
		log.Printf("db: update_status id=%d status=%s error=%q", id, st, errMsg)
	} else {
		log.Printf("db: update_status id=%d status=%s", id, st)
	}
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
	log.Printf("db: update_meta id=%d title=%q duration=%d thumbnail_set=%t", id, title, duration, thumbnail != "")
	return nil
}

// ListDownloads returns downloads filtered and sorted.
type ListFilter struct {
	Status string // optional: pending|downloading|completed|error
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
	sb.WriteString("SELECT id, url, title, duration, thumbnail_url, status, progress, filename, created_at, updated_at FROM downloads")
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
		if err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, &d.Status, &d.Progress, &filename, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Filename = filename.String
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateFilename sets the filename when download is complete.
func (s *Store) UpdateFilename(ctx context.Context, id int64, filename string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET filename = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, filename, id)
	if err != nil {
		return err
	}
	log.Printf("db: update_filename id=%d filename=%q", id, filename)
	return nil
}

// DeleteDownload removes a download record from the database.
func (s *Store) DeleteDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id = ?`, id)
	if err != nil {
		return err
	}
	log.Printf("db: delete_download id=%d", id)
	return nil
}

// IsURLCompleted checks if a URL already exists with status "completed"
func (s *Store) IsURLCompleted(ctx context.Context, url string) (bool, error) {
	if url == "" {
		return false, errors.New("empty_url")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE url = ? AND status = 'completed'`, url).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetPendingDownloads returns downloads with "pending" status, ordered by creation time
func (s *Store) GetPendingDownloads(ctx context.Context, limit int) ([]Download, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT id, url, title, duration, thumbnail_url, status, progress, filename, created_at, updated_at 
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
		err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, 
						 &d.Status, &d.Progress, &filename, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if filename.Valid {
			d.Filename = filename.String
		}
		downloads = append(downloads, d)
	}
	return downloads, rows.Err()
}

// GetIncompleteDownloads returns downloads that are not completed (status != 'completed' OR progress != 100)
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
	query := `SELECT id, url, title, duration, thumbnail_url, status, progress, filename, created_at, updated_at 
			  FROM downloads 
			  WHERE status != 'completed' OR progress < 100
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
		err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, 
						 &d.Status, &d.Progress, &filename, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if filename.Valid {
			d.Filename = filename.String
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

func normalizeStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "queued":
		return "pending"
	case "downloading", "completed", "pending":
		return s
	case "failed", "error":
		return "error"
	default:
		return "pending"
	}
}
