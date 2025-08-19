# Minimal Makefile for videofetch

BIN ?= videofetch
CMD ?= ./cmd/videofetch
HOST ?= 0.0.0.0
PORT ?= 8080
OUTPUT_DIR ?= ./downloads

.PHONY: build test run

build:
	go build -o $(BIN) $(CMD)

test:
	go test ./... -race

run: build
	@command -v yt-dlp >/dev/null 2>&1 || { echo "yt-dlp not found on PATH" >&2; exit 1; }
	@mkdir -p $(OUTPUT_DIR)
	./$(BIN) --output-dir $(OUTPUT_DIR) --host $(HOST) --port $(PORT)

