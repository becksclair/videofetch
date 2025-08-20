# CLAUDE.md (for coding agents)

Opinionated, fast, and low‑drama defaults for coding agents working on this repo. If this repository defines a more specific convention, the repo wins.

## 0) Purpose & Scope

- How to think, decide, and ship changes in this project.
- Keep dependencies minimal; performance and testability first.

## 1) Fast Facts

- Name: Rebecca (Becks) Clair · Email: <rebecca@heliasar.com> · GitHub: becksclair · Site: heliasar.com
- OS: Linux/macOS; Editors: VS Code & Neovim; Shell: fish
- Communication: concise plans → confirm → execute; surface trade‑offs; calm pragmatism.

## 2) Prime Directives

- Propose → confirm → execute: share a short plan for cross‑cutting or risky work (new deps, DB/schema changes, CI, auth, public API changes).
- Minimize deps: prefer stdlib and small, well‑maintained libs. Build trivial utilities in‑repo (≤100 LOC).
- Performance first: choose data structures deliberately; measure and eliminate needless allocations.
- Tests are proof: user‑impacting changes gain/extend tests (unit/integration).
- Reproducibility: script setup; pin versions where it reduces flakiness.
- Security: no secrets in repo; use env files for local, least‑privilege configs.

## 3) Repo Specifics (Stack & Structure)

- Service: Go 1.23 web service using stdlib `net/http`.
- Downloader: external `yt-dlp` via subprocess, with robust progress parsing and fallbacks.
- Persistence: SQLite (modernc.org/sqlite) always enabled; default DB path under OS cache (`videofectch.db`).
- UI: Server‑rendered dashboard via `github.com/a-h/templ` + HTMX; CSS via Tailwind v4 built into `static/style.css`.
- Layout:
  - `cmd/videofetch/`: entrypoint (`main.go`).
  - `internal/server/`: HTTP routes, middleware, rate limiter, tests.
  - `internal/download/`: queue manager, workers, yt-dlp invocation.
  - `internal/store/`: SQLite schema and CRUD.
  - `internal/integration/`: opt‑in tests using real downloads.
  - `static/`: assets including `style.css`; `src/css/input.css` as Tailwind input.

## 4) Build, Run, Test

- Build: `go build -o videofetch ./cmd/videofetch`
- Run: `./videofetch --output-dir ./downloads --port 8080 --host 0.0.0.0`
  - Requires `yt-dlp` on `PATH` and support for `--progress-template`.
- Make (optional): `make run` builds CSS+templ and runs; `make test`; `make tools` to install `templ`.
- Unit tests: `go test ./... -race` (no network)
- Integration tests: `go test -tags=integration ./internal/integration -v`
  - Env: `INTEGRATION_URL=...` or `INTEGRATION_URLS="https://u1, https://u2"`

## 5) API & Behavior Snapshot

- REST:
  - `POST /api/download_single` → enqueue one URL; returns `{status,id,db_id?}`
  - `POST /api/download` → enqueue batch; returns `{status,ids,db_ids?}`
  - `GET /api/status[?id=...]` → in‑memory snapshot of active/queued items
  - `GET /api/downloads` → persisted listing (SQLite)
  - `GET /healthz` → `ok`
- Rate limiting: 60 req/min/IP.
- Queue/backpressure: bounded queue (`--queue`, default 128); `queue_full` on saturation.
- Fallbacks: automatic retry with safer `-f` selections and optional `--impersonate`.
- Graceful shutdown: stop accepting, drain HTTP, cancel in‑flight, close DB.

## 6) Coding Style & Tooling

- Indentation: tabs, width 4, LF (see `.editorconfig`).
- Format/lint: `go fmt ./...`, `go vet ./...` (optionally `golangci-lint run`).
- Logging: `log.Printf` with concise, structured-ish messages.
- TS/CSS (optional): Tailwind v4 via Bun (`bun run build-css`). No JS build for the dashboard.

## 7) Testing Strategy

- Unit: focus on HTTP handlers and state transitions (`internal/server`), `httptest`, table tests, no network.
- Integration: real `yt-dlp` + network behind `-tags=integration`. Keep URLs stable/small.
- Always run with `-race` locally.
- Always run integration tests locally.

## 8) Security & Configuration

- No secrets in repo. Don’t hardcode credentials or private URLs in tests.
- Validate URLs (HTTP/HTTPS only). Respect the built‑in rate limiter.
- Env/flags: `VIDEOFETCH_YTDLP_FORMAT`, `VIDEOFETCH_YTDLP_IMPERSONATE`; CLI flags override env.

## 9) Project Hygiene

- Conventional Commits; small, focused PRs.
- PRs include: purpose, user impact, flags/env vars, risks/rollback, sample requests/responses as needed.
- Keep docs updated (README, dev notes). Ensure format/lint/tests are clean.

## 10) Agent Workflow (You)

- Before: read repo conventions; confirm acceptance criteria and perf envelope; post a short plan for risky changes.
- During: keep diffs focused; write/extend tests alongside changes; log minimally.
- After: run all checks; summarize trade‑offs and future work in PR body.

## 11) Quick API Smoke

```bash
curl -X POST localhost:8080/api/download_single \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/video.mp4"}'
```
