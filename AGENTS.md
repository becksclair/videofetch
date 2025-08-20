# AGENTS.md (for coding agents)

Opinionated, fast, and low‑drama defaults for coding agents working on the VideoFetch project. If this repository defines a more specific convention, the repo wins.

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

- **Service**: Go 1.23+ web service using stdlib `net/http`
- **Downloader**: External `yt-dlp` via subprocess with robust progress parsing and metadata extraction
- **Persistence**: SQLite (`modernc.org/sqlite`) always enabled; default DB path under OS cache (`videofetch.db`)
- **UI**: Server‑rendered dashboard via `github.com/a-h/templ` + HTMX; CSS via Tailwind v4 → `static/style.css`
- **Dependencies**: Minimal - only `templ`, `sqlite`, and dev tools (Bun for CSS)
- **Layout**:
  - `cmd/videofetch/`: CLI entrypoint and flags
  - `internal/server/`: HTTP routes, middleware, rate limiter, dashboard handlers
  - `internal/download/`: Download manager, worker pool, yt-dlp integration, metadata hooks
  - `internal/store/`: SQLite schema, CRUD operations, migrations
  - `internal/ui/`: Templ templates and helpers for dashboard
  - `internal/integration/`: Real network tests with yt-dlp
  - `static/`: Static assets (CSS, icons); `src/css/input.css` for Tailwind input

## 4) Build, Run, Test

- Build: `go build -o videofetch ./cmd/videofetch`
- Run: `./videofetch --output-dir ./downloads --port 8080 --host 0.0.0.0`
  - Requires `yt-dlp` on `PATH` and support for `--progress-template`.
- **Make targets**:
  - `make run`: builds CSS + templ + runs server
  - `make test`: runs unit tests with race detection
  - `make tools`: installs `templ` CLI
  - `make generate`: regenerates templ files + rebuilds CSS
- **Unit tests**: `go test ./... -race` (no network, mocked dependencies)
- **Integration tests**: `go test -tags=integration ./internal/integration -v`
  - Env vars: `INTEGRATION_URL=...` or `INTEGRATION_URLS="url1, url2"`
  - Tests real yt-dlp + network; includes metadata extraction and DB persistence

## 5) API & Behavior Snapshot

- **REST API**:
  - `POST /api/download_single` → enqueue single URL with metadata prefetch; returns `{status,id,db_id}`
  - `POST /api/download` → enqueue batch URLs; returns `{status,ids,db_ids}`
  - `GET /api/status[?id=...]` → real-time queue snapshot (in-memory)
  - `GET /api/downloads` → persisted download history (SQLite) with filtering/sorting
  - `GET /dashboard`, `/` → web dashboard (Templ + HTMX)
  - `GET /healthz` → health check
- **Rate limiting**: 60 req/min per client IP
- **Queue management**: Bounded queue (`--queue`, default 128); returns `queue_full` on saturation
- **Download features**:
  - Automatic metadata extraction (title, duration, thumbnail) before download
  - Real-time progress tracking via custom yt-dlp progress template
  - Default format selection with embedded subtitles, metadata, thumbnails, chapters
- **Graceful shutdown**: Stop accepting requests → drain HTTP → cancel downloads → close DB

## 6) Coding Style & Tooling

- **Indentation**: tabs, width 4, LF (see `.editorconfig`)
- **Format/lint**: `go fmt ./...`, `go vet ./...` (optionally `golangci-lint run`)
- **Logging**: `log.Printf` with structured messages; minimal and purposeful
- **CSS build**: Tailwind v4 via Bun (`bun run build-css` → `static/style.css`)
- **No client JS**: Dashboard uses pure HTMX for interactivity; no build step required

## 7) Testing Strategy

- **Unit tests**: HTTP handlers, state transitions, business logic
  - Use `httptest.ResponseRecorder` for HTTP tests
  - Table-driven tests for multiple scenarios
  - Mock external dependencies (no network/filesystem)
  - Always run with `-race` flag
- **Integration tests**: Real yt-dlp + network (behind `-tags=integration`)
  - Test metadata extraction workflows
  - Test database persistence end-to-end
  - Use small, stable test URLs
  - Include progress tracking and error scenarios
- **Coverage**: Track test coverage; aim for high coverage on critical paths

## 8) Security & Configuration

- **No secrets**: Never commit credentials, API keys, or private URLs
- **URL validation**: Only allow HTTP/HTTPS URLs; reject file://, ftp://, etc.
- **Rate limiting**: Respect built-in rate limiter (60 req/min/IP)
- **Input validation**: Sanitize all user inputs; limit request body sizes
- **Safe defaults**: Use least-privilege file permissions and secure temp directories

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
# Single download
curl -X POST localhost:8080/api/download_single \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}'

# Check status
curl localhost:8080/api/status

# View dashboard
open http://localhost:8080/dashboard
```
