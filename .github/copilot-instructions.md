# Copilot Project Instructions

Concise guidance for AI agents working in this repo. Focus on existing patterns—do not introduce new frameworks.

## Architecture & Responsibilities

- Entry: `cmd/videofetch/main.go` wires flags, validates `--output-dir`, checks `yt-dlp`, builds `download.Manager`, mounts HTTP server via `internal/server`.
- Download pipeline: `internal/download/manager.go` provides a bounded job queue + worker pool. Public surface: `NewManager`, `Enqueue(url)`, `Snapshot(id)`, `StopAccepting()`, `Shutdown()`. Internal workers call `yt-dlp` and parse progress lines (regex `percentRe`) from stdout/stderr.
- HTTP layer: `internal/server/server.go` builds a `http.ServeMux` with JSON endpoints `/api/download_single`, `/api/download`, `/api/status`, plus `/healthz`. Inline rate limiting via `ipRateLimiter` (60 req/min/IP). Validation: only HTTP/HTTPS URLs, size-limited JSON bodies, maps domain errors to response messages.
- States: `queued|downloading|completed|failed`; progress 0–100. Failures truncate error messages to 512 chars.

## Key Conventions

- Errors surfaced to clients use stable message strings: `invalid_request`, `invalid_url`, `queue_full`, `shutting_down`, `internal_error`, `rate_limited`, `no_valid_urls`, `method_not_allowed`.
- Manager returns raw string errors (`queue_full`, `shutting_down`); server layer maps them to status codes.
- Progress only ever increases (see `updateProgress` guard) even if `yt-dlp` prints multi-phase percentages.
- Worker & queue sizing controlled by flags `--workers` and `--queue` with sensible fallbacks; keep new features flag-driven if tunable.
- Rate limiting keyed by client IP using headers (`X-Forwarded-For`, `X-Real-IP`) before `RemoteAddr`.

## Build / Run / Test

- Build: `go build -o videofetch ./cmd/videofetch` (or `make build`). Requires Go 1.25.
- Run (pre-flight ensures `yt-dlp` present): `./videofetch --output-dir ./downloads` (plus optional `--port`, `--host`, `--workers`, `--queue`).
- Unit tests (fast, no network): `go test ./... -race` (covers handlers, limiter, error mapping via `internal/server/server_test.go`).
- Integration tests (real downloads, opt-in): `go test -tags=integration ./internal/integration -v` with optional env `INTEGRATION_URL` or `INTEGRATION_URLS` (comma/space separated). Skip automatically if `yt-dlp` missing.

## Adding Functionality

- Prefer extending existing handler switch blocks; keep JSON response shape: `{ "status": "success|error", ... }` with `Content-Type: application/json`.
- For new download states/metadata, add fields to `download.Item` (JSON tags already present) and ensure `Snapshot` copies by value to avoid data races.
- When altering queue semantics, preserve non-blocking enqueue + `queue_full` behavior (bounded backpressure vs. unbounded growth).
- If adding external commands, mirror `CheckYTDLP` pattern for early validation.

## Safety & Robustness

- Do not log full external command errors beyond existing truncation (512 chars) to avoid log bloat.
- Keep timeouts consistent: server has conservative read timeouts; write timeout intentionally unlimited (streaming/progress). Preserve `WriteTimeout: 0` unless redesigning streaming.
- Avoid introducing global state; pass dependencies explicitly (mirroring `New(mgr)` pattern).

## Examples

- Enqueue single: `curl -X POST :8080/api/download_single -d '{"url":"https://example.com/v.mp4"}' -H 'Content-Type: application/json'`.
- Poll status: `curl :8080/api/status?id=<id>` returns array with one item; omit `id` for all.

## When Unsure

- Mirror existing error wording & structure; add new error codes only if surfaced to clients with tests.
- Check tests for canonical behaviors before refactoring (e.g., rate limiting expectations, method not allowed handling).

(End of instructions — keep additions within these established patterns.)
