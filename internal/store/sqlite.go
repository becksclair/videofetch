package store

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
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
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
}

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
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_downloads_status ON downloads(status);
CREATE INDEX IF NOT EXISTS idx_downloads_created_at ON downloads(created_at);
`
    _, err := db.Exec(ddl)
    return err
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
    id, _ := res.LastInsertId()
    return id, nil
}

// UpdateProgress sets progress and bumps updated_at.
func (s *Store) UpdateProgress(ctx context.Context, id int64, progress float64) error {
    _, err := s.db.ExecContext(ctx, `UPDATE downloads SET progress = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, progress, id)
    return err
}

// UpdateStatus sets status (and optionally title/thumbnail if provided) and bumps updated_at.
func (s *Store) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
    st := normalizeStatus(status)
    _, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, st, id)
    return err
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
    return err
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
    sb.WriteString("SELECT id, url, title, duration, thumbnail_url, status, progress, created_at, updated_at FROM downloads")
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
        if err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.Duration, &d.ThumbnailURL, &d.Status, &d.Progress, &d.CreatedAt, &d.UpdatedAt); err != nil {
            return nil, err
        }
        out = append(out, d)
    }
    return out, rows.Err()
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

