# Minimal Makefile for videofetch

BIN ?= videofetch
CMD ?= ./cmd/videofetch
HOST ?= 0.0.0.0
PORT ?= 8080
OUTPUT_DIR ?= ./downloads

.PHONY: build test run generate tools

build: generate
	go build -o $(BIN) $(CMD)

test:
	go test ./... -race
	go test -tags=integration ./internal/integration -v

run: build
	@command -v yt-dlp >/dev/null 2>&1 || { echo "yt-dlp not found on PATH" >&2; exit 1; }
	@mkdir -p $(OUTPUT_DIR)
	./$(BIN) --output-dir $(OUTPUT_DIR) --host $(HOST) --port $(PORT)

# Generate Templ components (optional for development)
generate:
	bun run build-css
	@command -v templ >/dev/null 2>&1 || { echo "templ not found; run 'make tools' first" >&2; exit 1; }
	templ generate

# Install codegen tools
tools:
	go install github.com/a-h/templ/cmd/templ@latest
