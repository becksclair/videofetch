# VideoFetch Agent Instructions

## Build/Test Commands
- **Build**: `go build -o videofetch ./cmd/videofetch` or `make build`
- **Test all**: `go test ./... -race`
- **Integration tests**: `go test -tags=integration ./internal/integration -v`
- **Single test**: `go test -run TestName ./internal/package`
- **Run dev**: `make run` or `./videofetch --port 8080 --host 0.0.0.0`
- **CSS build**: `bun run build-css`

## Architecture
Go 1.23+ service with SQLite DB. Core: download manager (worker pool + bounded queue), HTTP server (JSON API + dashboard), yt-dlp integration. Templ + HTMX frontend. Structured JSON logging. Rate limiting (60 req/min/IP).

## Code Style
Use stable error messages: `invalid_request`, `queue_full`, etc. Progress only increases monotonically. Flag-driven features. Pass dependencies explicitly (no globals). Interfaces for abstraction. JSON tags on structs. Bounded queues over unbounded. Structured logging with event types.
