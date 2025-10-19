# Minimal Makefile for videofetch

BIN ?= videofetch
CMD ?= ./cmd/videofetch
HOST ?= 0.0.0.0
PORT ?= 8080
OUTPUT_DIR ?= $(HOME)/Videos/videofetch
INSTALL_DIR ?= $(HOME)/.local/bin
SERVICE_DIR ?= $(HOME)/.config/systemd/user

.PHONY: build test run generate tools install uninstall

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

# Install binary and systemd service
install: build
	@mkdir -p $(INSTALL_DIR)
	@mkdir -p $(SERVICE_DIR)
	cp $(BIN) $(INSTALL_DIR)/
	cp videofetch.service $(SERVICE_DIR)/
	systemctl --user daemon-reload
	@echo "Installation complete. To start the service:"
	@echo "  systemctl --user enable videofetch.service"
	@echo "  systemctl --user start videofetch.service"

# Uninstall binary and systemd service
uninstall:
	systemctl --user stop videofetch.service 2>/dev/null || true
	systemctl --user disable videofetch.service 2>/dev/null || true
	rm -f $(INSTALL_DIR)/$(BIN)
	rm -f $(SERVICE_DIR)/videofetch.service
	systemctl --user daemon-reload
	@echo "Uninstallation complete."
