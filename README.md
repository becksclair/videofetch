# VideoFetch Service

Go-based web service for downloading videos via `yt-dlp` with a simple REST API, a concurrent download queue, progress tracking, and rate limiting.

## Requirements

- Go 1.25+
- `yt-dlp` installed and available on `PATH`
  - Requires a recent version supporting `--progress-template` (for stable progress parsing).

## Run

```shell
go build -o videofetch ./cmd/videofetch
./videofetch --output-dir /path/to/downloads --port 8080 --host 0.0.0.0

# Optional: control yt-dlp behavior via env instead of CLI flags:
#   export VIDEOFETCH_YTDLP_FORMAT='bestvideo*+bestaudio/best'
#   export VIDEOFETCH_YTDLP_FORMAT='bestvideo*[ext=mp4]+bestaudio[ext=m4a]/best'
#   export VIDEOFETCH_YTDLP_IMPERSONATE='chrome'
# Then run the server as above.
```

Flags:

- `--output-dir` (required): output directory for downloads (created if missing)
- `--port` (default: `8080`)
- `--host` (default: `0.0.0.0`)
- `--yt-dlp-format` (default: `bestvideo*+bestaudio/best`):
  yt-dlp format selector passed as `-f`. CLI flag overrides `VIDEOFETCH_YTDLP_FORMAT`.
- `--yt-dlp-impersonate` (default: empty): yt-dlp `--impersonate` client, e.g.,
  `chrome` or `chrome:windows-10`. CLI flag overrides `VIDEOFETCH_YTDLP_IMPERSONATE`.

## API

Base URL: `http://HOST:PORT`

### POST /api/download_single

Request:

```json
{
  "url": "https://video-site.com/watch?v=example"
}
```

Response:

```json
{
  "status": "success|error",
  "message": "string",
  "id": "string"
}
```

### POST /api/download

Request:

```json
{
  "urls": ["https://...", "https://..."]
}
```

Response:

```json
{
  "status": "success|error",
  "message": "string",
  "ids": ["string"]
}
```

### GET /api/status[?id=<download-id>]

Response:

```json
{
  "status": "success|error",
  "downloads": [
    { "id": "...", "url": "...", "progress": 0-100, "state": "queued|downloading|completed|failed", "error": "string" }
  ]
}
```

## Error codes/messages

- `invalid_request`: malformed JSON body or missing fields
- `invalid_url`: URL is missing or not http/https
- `yt_dlp_not_found`: `yt-dlp` not installed or not in PATH
- `queue_full`: server queue is full; retry later
- `not_found`: no download with given ID
- `internal_error`: unexpected server error

## Notes

- The service runs a worker pool to handle concurrent downloads and uses a buffered queue for backpressure.
- Progress is parsed from `yt-dlp` progress output; when the process completes successfully, progress becomes `100` and state `completed`.
- On shutdown, the server stops accepting new requests and cancels in-flight downloads gracefully.

## Testing

- Unit tests (HTTP handlers): `go test ./... -race`
- Integration tests (real `yt-dlp`, network):
  - Requires `yt-dlp` in `PATH` and outbound network access.
  - Run with build tag: `go test -tags=integration ./internal/integration -v`
  - Options:
    - Single URL override: `INTEGRATION_URL=https://...`
    - Batch URLs override: `INTEGRATION_URLS="https://u1, https://u2 ..."`
  - Notes: Some providers (e.g., YouTube) may restrict certain formats; if you
    observe `yt-dlp: exit status 1` errors, try overriding the format using
    `--yt-dlp-format`/`VIDEOFETCH_YTDLP_FORMAT` and/or impersonating a browser via
    `--yt-dlp-impersonate chrome` or `VIDEOFETCH_YTDLP_IMPERSONATE=chrome`.

## Dashboard (Templ + HTMX)

- Visit `http://HOST:PORT/dashboard` (or `/`) for a simple dashboard.
- Includes an enqueue form and a live-updating queue table (polls every 1s).
- Server-rendered using `github.com/a-h/templ`, updated via HTMX; no client build step.
  - Progress parsing uses yt-dlp `--progress-template` with a custom marker for robust updates.

Dev notes:
- The generated Go from `.templ` is committed. If you modify templates, regenerate with:
  - `make tools` (one-time) then `make generate`
