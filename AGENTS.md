# Repository Guidelines

This repo implements a small Go web service that queues and downloads videos via `yt-dlp`, exposes a REST API, and tracks progress.

## Project Structure & Modules
- `cmd/videofetch/`: entrypoint (`main.go`).
- `internal/server/`: HTTP routes, middleware, rate limiter, tests.
- `internal/download/`: queue manager, workers, `yt-dlp` invocation.
- `internal/integration/`: opt‑in integration tests (real downloads).
- Binary output: `./videofetch` (see Build).

## Build, Test, Run
- Build: `go build -o videofetch ./cmd/videofetch`
  - Produces `./videofetch`.
- Run: `./videofetch --output-dir ./downloads --port 8080 --host 0.0.0.0`
  - Requires `yt-dlp` on `PATH`.
- Unit tests: `go test ./... -race`
- Integration tests: `go test -tags=integration ./internal/integration -v`
  - Env: `INTEGRATION_URL=https://...` or `INTEGRATION_URLS="https://u1, https://u2"`.

## Coding Style & Conventions
- Indentation: tabs, width 4, LF (see `.editorconfig`).
- Go formatting: `gofmt`/`go fmt ./...` before pushing.
- Static checks: `go vet ./...` (optionally `golangci-lint run` if available).
- Naming: packages lower_snake; exported identifiers use Go conventions (`CamelCase`). Tests: `*_test.go`, `TestXxx`.

## Testing Guidelines
- Unit tests focus on HTTP handlers and state transitions (`internal/server`).
- Avoid network in unit tests; use table tests, `httptest` and fakes.
- Integration tests require `yt-dlp` and network; keep URLs small and stable.
- Run with `-race`. Add tests alongside changes that affect user‑visible API or download logic.

## Commit & PR Guidelines
- Commits: Conventional Commits (e.g., `feat(server): add batch enqueue`), small, focused.
- PRs include: purpose, user impact, flags/env vars touched, and risks/rollback.
- Link issues; attach relevant logs or sample API requests/responses.
- CI/format must be clean; tests updated/added for behavior changes.

## Security & Configuration Tips
- No secrets in repo. Do not hardcode credentials or private URLs in tests.
- Validate URLs (HTTP/HTTPS only) and rely on built‑in rate limiting.
- Ensure `yt-dlp` is installed and up to date; avoid passing untrusted flags through.

## Quick API Smoke
```
curl -X POST localhost:8080/api/download_single \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/video.mp4"}'
```

