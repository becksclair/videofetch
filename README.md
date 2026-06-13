# VideoFetch

VideoFetch is a Go service for queueing video downloads through `yt-dlp`. It provides a JSON API, a small HTMX dashboard, SQLite-backed history, WebSocket updates, and an optional browser extension.

## Requirements

- Go 1.23+
- `yt-dlp` on `PATH` with `--progress-template` support
- Bun for CSS and browser-extension builds
- `templ` only when editing `internal/ui/*.templ`

## Build, Run, Test

```bash
make build
./videofetch --host 0.0.0.0 --port 8080
```

Default downloads go to `$HOME/Videos/videofetch`; the default database is `$HOME/.cache/videofetch/videofetch.db` on Linux/macOS and `%APPDATA%/videofetch/videofetch.db` on Windows.

Useful commands:

```bash
go build -o videofetch ./cmd/videofetch
go test ./cmd/... ./internal/... -race
go test -tags=integration ./internal/integration -v
bun run build-css
make generate
```

`make generate` rebuilds Tailwind CSS and regenerates committed templ output. Run `make tools` if `templ` is missing.

## CLI

- `--output-dir`: download directory, default `$HOME/Videos/videofetch`
- `--host`: bind host, default `0.0.0.0`
- `--port`: bind port, default `8080`
- `--workers`: concurrent download workers, default `4`
- `--queue`: queue capacity, default `128`
- `--db`: SQLite database path, default OS cache path
- `--log-level`: `debug`, `info`, `warn`, or `error`
- `--unsafe-log-payloads`: log raw API payloads in debug output

## API

All JSON endpoints return `{ "status": "success|error", ... }`.

### Queue Downloads

```http
POST /api/download_single
Content-Type: application/json

{ "url": "https://example.com/video" }
```

Success responses include `message: "enqueued"` and `db_id` when SQLite is enabled. Duplicate URLs return `message: "already_exists"` with `existing_id` and `existing_status`.

```http
POST /api/download
Content-Type: application/json

{ "urls": ["https://example.com/one", "https://example.com/two"] }
```

Batch responses include `db_ids`. Invalid URLs are skipped; if no valid URLs remain, the response message is `no_valid_urls`.

### Status and History

- `GET /api/status?id=<download-id>`: in-memory queue state; omit `id` for all active items.
- `GET /api/downloads`: persisted downloads.

`/api/downloads` query parameters:

- `status`: `active`, `history`, `pending`, `downloading`, `paused`, `completed`, `error`, or `canceled`
- `sort`: `created_at`, `updated_at`, `date`, `title`, `status`, or `progress`
- `order`: `asc` or `desc`
- `limit`, `offset`: pagination

### Control

All control requests use JSON body `{ "id": 123 }`, where `id` is the SQLite row ID.

- `POST /api/control/pause`
- `POST /api/control/resume`
- `POST /api/control/cancel`
- `POST /api/control/play`
- `DELETE /api/remove`: remove a download row without deleting files
- `DELETE /api/delete`: delete completed output files and remove the row
- `DELETE /api/history/clear`: remove completed/error/canceled rows
- `POST /api/retry_failed`: reset failed rows to pending
- `GET /api/download_file?id=123`: serve a completed output file

### WebSocket and Health

- `GET /api/ws/downloads`: sends a `snapshot`, then coalesced `diff` events and `heartbeat` frames. Accepts the same list filters as `/api/downloads`.
- `GET /healthz`: returns `ok`.

Common error messages: `invalid_request`, `invalid_url`, `queue_full`, `no_valid_urls`, `invalid_state`, `not_found`, `not_playable`, `file_not_found`, `delete_failed`, `player_launch_failed`, `shutting_down`, `method_not_allowed`, `internal_error`.

## Dashboard

Open `http://HOST:PORT/` or `/dashboard`.

The dashboard is server-rendered with templ and HTMX. It can enqueue URLs, list history, filter/sort rows, retry failed downloads, remove rows, and download completed files. Generated `internal/ui/*_templ.go` files are committed.

## Browser Extension

The React/Tailwind extension lives in `webext/`.

```bash
cd webext
bun install
bun run build:chrome
bun run build:firefox
bun run lint:firefox
```

Load `webext/dist/chrome` as an unpacked Chrome extension, or load `webext/dist/firefox` in Firefox from `about:debugging#/runtime/this-firefox`.

Useful extension scripts:

- `bun run run:firefox`: build and run in Firefox with `web-ext`
- `bun run sign:firefox`: sign an XPI using AMO credentials from the environment or `~/personal/dotfiles/secrets.sh`

## systemd User Service

```bash
make install
systemctl --user enable --now videofetch.service
curl -fsS http://127.0.0.1:8080/healthz
```

The service binds to `127.0.0.1:8080`, downloads to `$HOME/Videos/videofetch`, and writes the database under `$HOME/.cache/videofetch`.

## Architecture Notes

- `cmd/videofetch/main.go`: flags, config, logger, store, manager, server wiring, shutdown.
- `internal/download`: bounded queue, worker pool, yt-dlp execution, monotonic progress, resume/retry coordination.
- `internal/store`: SQLite schema and typed helpers for history, control state, and artifact paths.
- `internal/server`: JSON API, dashboard handlers, static files, WebSocket stream, logging middleware.
- `internal/ui`: templ dashboard components and display helpers.
- `webext`: browser extension source.

Operational constraints worth preserving:

- Client-facing error strings are stable contracts.
- Progress should not decrease.
- Queue capacity is bounded; do not replace backpressure with unbounded growth.
- Pass dependencies explicitly; avoid package-level mutable state.
- Raw URLs and request bodies should stay redacted unless `--unsafe-log-payloads` is enabled.
