# VideoFetch Service

Go-based web service for downloading videos via `yt-dlp` with a simple REST API, a concurrent download queue, progress tracking, a small dashboard, and rate limiting.

## Requirements

- Go 1.23+
- `yt-dlp` installed and available on `PATH`
  - Must support `--progress-template` (checked at startup).

## Quick start

```bash
go build -o videofetch ./cmd/videofetch
./videofetch --output-dir ./downloads --port 8080 --host 0.0.0.0

# Optional: control yt-dlp via env instead of flags
# export VIDEOFETCH_YTDLP_FORMAT='bestvideo*+bestaudio/best'
# export VIDEOFETCH_YTDLP_IMPERSONATE='chrome'  # or 'chrome:windows-10'
```

## CLI flags

- `--output-dir` (required): output directory for downloads (created if missing)
- `--port` (default: `8080`)
- `--host` (default: `0.0.0.0`)
- `--workers` (default: `4`): concurrent download workers
- `--queue` (default: `128`): queue capacity (backpressure)
- `--yt-dlp-format` (default: `bestvideo*+bestaudio/best`): passed to yt-dlp as `-f`; overrides `VIDEOFETCH_YTDLP_FORMAT`
- `--yt-dlp-impersonate` (default: empty): passed to yt-dlp as `--impersonate`; overrides `VIDEOFETCH_YTDLP_IMPERSONATE`
- `--db` (optional): SQLite database path; defaults to OS cache dir at `videofetch/videofectch.db`
  - Windows: `%APPDATA%/videofetch/videofectch.db`
  - Linux/macOS: `$HOME/.cache/videofetch/videofectch.db`

Notes:

- The database is always enabled; omitting `--db` uses the default path above.
- Rate limiting: 60 requests/minute per client IP.

## API

Base URL: `http://HOST:PORT`

### POST `/api/download_single`

Request:

```json
{ "url": "https://video-site.com/watch?v=example" }
```

Response:

```json
{ "status": "success|error", "message": "string", "id": "string", "db_id": 123 }
```

### POST `/api/download`

Request:

```json
{ "urls": ["https://...", "https://..."] }
```

Response:

```json
{ "status": "success|error", "message": "string", "ids": ["..."], "db_ids": [123, 456] }
```

### GET `/api/status[?id=<download-id>]`

Response:

```json
{
  "status": "success",
  "downloads": [
    {
      "id": "...",
      "url": "...",
      "progress": 0,
      "state": "queued|downloading|completed|failed",
      "error": "",
      "title": "optional",
      "duration": 0,
      "thumbnail_url": "optional"
    }
  ]
}
```

### GET `/api/downloads`

Lists persisted downloads from SQLite.

Query params: `status=pending|downloading|completed|error`, `sort=created_at|title|status`, `order=asc|desc`.

Response:

```json
{
  "status": "success",
  "downloads": [
    {
      "id": 1,
      "url": "...",
      "title": "...",
      "duration": 123,
      "thumbnail_url": "...",
      "status": "downloading",
      "progress": 42.0,
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### GET `/healthz`

Health check; returns `ok`.

## Error codes/messages

- `invalid_request`: malformed JSON body or missing fields
- `invalid_url`: URL is missing or not http/https
- `yt_dlp_not_found`: `yt-dlp` not installed or missing `--progress-template`
- `queue_full`: server queue is full; retry later
- `shutting_down`: server is draining; try again later
- `internal_error`: unexpected server error

## Dashboard (Templ + HTMX)

- Visit `http://HOST:PORT/dashboard` (or `/`) for a simple dashboard.
- Includes an enqueue form and a live-updating queue table (polls every 1s).
- Server-rendered using `github.com/a-h/templ`, updated via HTMX; no client build step.

Dev notes:

- The generated `.templ` Go is committed. If you modify templates:
  - `make tools` (one-time), then `make generate` to re-generate Templ and rebuild CSS.
- CSS is built from Tailwind v4 via `bun run build-css` into `./static/style.css`.

## Testing

- Unit tests (handlers, state): `go test ./... -race`
- Integration tests (real `yt-dlp`, network):
  - `go test -tags=integration ./internal/integration -v`
  - Env overrides:
    - `INTEGRATION_URL=https://...`
    - `INTEGRATION_URLS="https://u1, https://u2"`
  - Tips: Some providers restrict certain formats; if you see `yt-dlp: exit status 1`, try `--yt-dlp-format`/`VIDEOFETCH_YTDLP_FORMAT` and/or `--yt-dlp-impersonate`.

## Behavior & notes

- Worker pool for concurrency; bounded queue for backpressure.
- Progress parsed from `yt-dlp` via a custom `--progress-template` prefix; on success progress reaches 100 and state `completed`.
- Fallbacks: on common provider/format errors, the service retries with safer `-f` selections and optional impersonation.
- Graceful shutdown: stops accepting new jobs, drains HTTP, cancels in-flight downloads, and closes the DB.
