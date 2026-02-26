# VideoFetch Service

Go-based web service for downloading videos via `yt-dlp` with a simple REST API, a concurrent download queue, progress tracking, a small dashboard, and rate limiting.

## Requirements

- Go 1.23+
- `yt-dlp` installed and available on `PATH`
  - Must support `--progress-template` (checked at startup).

## Quick start

```bash
go build -o videofetch ./cmd/videofetch
# By default, outputs to $HOME/Videos/videofetch
./videofetch --port 8080 --host 0.0.0.0

```

## CLI flags

- `--output-dir` (optional): output directory for downloads (default: `$HOME/Videos/videofetch`, created if missing)
- `--port` (default: `8080`)
- `--host` (default: `0.0.0.0`)
- `--workers` (default: `4`): concurrent download workers
- `--queue` (default: `128`): queue capacity (backpressure)
- `--db` (optional): SQLite database path; defaults to OS cache dir at `videofetch/videofetch.db`
  - Windows: `%APPDATA%/videofetch/videofetch.db`
  - Linux/macOS: `$HOME/.cache/videofetch/videofetch.db`
- `--log-level` (default: `info`): Log level for structured JSON logging (`debug`, `info`, `warn`, `error`)
- `--unsafe-log-payloads` (default: `false`): allow raw API payload dumps in debug logs (unsafe; may expose secrets)

Notes:

- The database is always enabled; omitting `--db` uses the default path above.
- Rate limiting: 60 requests/minute per client IP.
- Logging outputs structured JSON to stdout, suitable for aggregation systems (ELK, CloudWatch, etc.)
- URL fields in logs are redacted by default (userinfo stripped, query values masked).

## API

Base URL: `http://HOST:PORT`

### POST `/api/download_single`

Enqueue a single video URL for download. Automatically fetches video metadata (title, duration, thumbnail) when possible.

Request:

```json
{ "url": "https://video-site.com/watch?v=example" }
```

Response:

```json
{ "status": "success|error", "message": "enqueued|already_exists", "db_id": 123, "existing_id": 123, "existing_status": "pending|downloading|paused|completed|error|canceled" }
```

### POST `/api/download`

Enqueue multiple video URLs for download in batch.

Request:

```json
{ "urls": ["https://...", "https://..."] }
```

Response:

```json
{ "status": "success|error", "message": "string", "ids": ["..."], "db_ids": [123, 456] }
```

### GET `/api/status[?id=<download-id>]`

Get real-time status of downloads from the in-memory queue. Use `id` parameter to filter by specific download.

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

Lists persisted downloads from SQLite database with filtering and sorting.

Query params: `status=pending|downloading|paused|completed|error|canceled`, `sort=created_at|title|status`, `order=asc|desc`, `limit=<n>`, `offset=<n>`.

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
      "filename": "optional",
      "artifact_paths": ["optional absolute/relative tracked file paths"],
      "error_message": "optional",
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### DELETE `/api/remove`

Remove a download row from history only (does not delete output files).

Request:
```json
{ "id": 123 }
```

### DELETE `/api/delete`

Delete a completed download's output file(s) and remove its history row.

Request:
```json
{ "id": 123 }
```

Notes:
- Only valid for `completed` rows.
- If file deletion fails, the row is kept and the endpoint returns `delete_failed`.

### POST `/api/control/pause`
Pause a queued/downloading item by DB record ID.

Request:
```json
{ "id": 123 }
```

### POST `/api/control/resume`
Resume a paused/canceled/error item by DB record ID.

Request:
```json
{ "id": 123 }
```

### POST `/api/control/cancel`
Cancel an in-flight or queued item by DB record ID.

Request:
```json
{ "id": 123 }
```

### POST `/api/control/play`
Launch the completed file in the server host's default media player.

Request:
```json
{ "id": 123 }
```

### GET `/api/ws/downloads`
WebSocket stream for realtime download updates. Supports the same list query params as `/api/downloads` (for example `limit`, `offset`, `status`).

Event types:
- `snapshot`: full list on connect
- `diff`: incremental changes with `upserts` and `deletes` (coalesced over a short server window to reduce chatter)
- `heartbeat`: keepalive frame

### GET `/healthz`

Health check endpoint; returns `ok`.

## Error codes/messages

- `invalid_request`: malformed JSON body or missing fields
- `invalid_url`: URL is missing or not http/https
- `yt_dlp_not_found`: `yt-dlp` not installed or missing `--progress-template`
- `queue_full`: server queue is full; retry later
- `invalid_state`: action is not valid for current row status
- `shutting_down`: server is draining; try again later
- `internal_error`: unexpected server error

## Dashboard (Templ + HTMX)

- Visit `http://HOST:PORT/dashboard` (or `/`) for a web dashboard
- Features:
  - Download form for single/batch URL submission
  - Real-time progress tracking (auto-refreshes every 1s)
  - Download history with filtering and sorting
  - Video metadata display (title, duration, thumbnails)
- Server-rendered using `github.com/a-h/templ` with HTMX for dynamic updates
- No client-side JavaScript build required

### Development

- Generated `.templ` Go files are committed to the repository
- To modify templates:
  - Install tools: `make tools`
  - Regenerate: `make generate`
- CSS built from Tailwind v4: `bun run build-css` → `./static/style.css`

## Testing

- **Unit tests** (handlers, state management): `go test ./... -race`
- **Integration tests** (real `yt-dlp` + network):
  - Run: `go test -tags=integration ./internal/integration -v`
  - Environment overrides:
    - `INTEGRATION_URL=https://...` (single test URL)
    - `INTEGRATION_URLS="https://u1, https://u2"` (multiple test URLs)
  - Tests include: metadata extraction, database persistence, download workflows
- **Coverage**: Generate with `go test -coverprofile=coverage.out ./...`

## Architecture & Features

### Core Components

- **Download Manager**: Worker pool with configurable concurrency and bounded queue
- **Progress Tracking**: Real-time parsing from `yt-dlp` using custom `--progress-template`
- **Database**: SQLite persistence for download history and metadata
- **Rate Limiting**: 60 requests/minute per client IP
- **Metadata Extraction**: Automatic fetching of video title, duration, and thumbnails

### Download Behavior

- Uses `yt-dlp` default format selection for maximum compatibility
- Includes embedded subtitles, metadata, thumbnails, and chapters
- Progress updates in real-time from 0-100%
- Automatic fallbacks for metadata extraction failures

## Chrome Extension (MV3 Sidepanel)

A React + Tailwind extension lives in `./webext` and provides:
- Sidepanel queue with realtime WebSocket updates (poll fallback)
- URL enqueue from sidepanel
- Toolbar-click enqueue for the current tab URL
- Keyboard shortcut to open side panel (default: `Alt+Z`)
- Keyboard shortcut to enqueue current page URL (default: `Alt+X`)
- Context-menu enqueue (link/media/page URL)
- Pause/resume/cancel/play/remove/delete actions
- Optional desktop notifications (completed/error/canceled)

Build steps:

```bash
cd webext
bun install
bun run build
```

Then load `webext/dist` as an unpacked extension in Chrome (`chrome://extensions`).

### Graceful Shutdown

1. Stop accepting new HTTP requests
2. Drain existing HTTP connections
3. Cancel in-flight downloads
4. Close database connections

### File Organization

- Downloaded files saved to `--output-dir` with original filenames
- Database stored in OS cache directory by default
- Static assets served from `./static/` directory

### Structured Logging

VideoFetch uses structured JSON logging for better observability and integration with log aggregation systems:

- **Format**: JSON lines to stdout with ISO8601 timestamps
- **Levels**: `DEBUG`, `INFO`, `WARN`, `ERROR` (set via `--log-level` flag)
- **Event Types**: Each log entry includes an `event` field for filtering:
  - `server_start`, `server_shutdown`: Server lifecycle events
  - `http_request`: HTTP request logging with method, path, duration
  - `download_*`: Download lifecycle (start, progress, complete, error)
  - `db_*`: Database operations (create, update, delete)
  - `metadata_*`: Metadata fetching operations
  - `ytdlp_*`: yt-dlp command execution
  - `dbworker_*`: Background worker operations

Example log entries:
```json
{"time":"2025-10-19T08:00:00Z","level":"INFO","msg":"server started","event":"server_start","addr":"0.0.0.0:8080","workers":4}
{"time":"2025-10-19T08:00:01Z","level":"INFO","msg":"download complete","event":"download_complete","download_id":"abc123","db_id":"42","filename":"video.mp4"}
{"time":"2025-10-19T08:00:02Z","level":"DEBUG","msg":"download progress","event":"download_progress","download_id":"abc123","progress":75.5}
