# VideoFetch Agent Instructions

## Commands

- Build: `go build -o videofetch ./cmd/videofetch` or `make build`
- Unit tests: `go test ./cmd/... ./internal/... -race`
- Integration tests: `go test -tags=integration ./internal/integration -v`
- Run: `make run` or `./videofetch --host 0.0.0.0 --port 8080`
- CSS: `bun run build-css`
- Templ/CSS generation: `make generate`
- Browser extension: run commands from `webext/` with Bun

## Project Shape

Go 1.23 service with SQLite persistence, a bounded download queue, yt-dlp workers, JSON API, HTMX dashboard, and a React browser extension.

Main paths:

- `cmd/videofetch`: process entrypoint and wiring
- `internal/download`: queue, worker pool, yt-dlp integration, progress/state handling
- `internal/store`: SQLite schema and query helpers
- `internal/server`: API, dashboard, WebSocket, static files, middleware
- `internal/ui`: templ components and generated Go output
- `webext`: browser extension source

## Conventions

- Keep client-facing error strings stable: `invalid_request`, `invalid_url`, `queue_full`, `invalid_state`, `not_found`, `internal_error`, and existing peers.
- Preserve monotonic progress.
- Keep queues bounded.
- Pass dependencies explicitly; avoid global mutable state.
- Use JSON tags on API/store structs.
- Keep tunable behavior flag-driven.
- Prefer existing interfaces and helper APIs before adding abstractions.
- Regenerate committed templ output after editing `.templ` files.
