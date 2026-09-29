BINARY := topswatch
IMAGE  := topswatch
TAG    ?= latest

.PHONY: build gui test lint clean docker run tui help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build the daemon binary (also contains --tui)
	go build -o $(BINARY) ./cmd/topswatch

# Build tags for the desktop viewer. The default compiles both the X11 and
# Wayland backends (needs libwayland-dev and libxkbcommon-dev as well as the
# X11 headers). Use GUI_TAGS="gui x11" for an X11-only build without the
# Wayland packages.
GUI_TAGS ?= gui

gui: ## Build the desktop viewer (cgo; see README "Desktop viewer" for packages)
	go build -tags "$(GUI_TAGS)" -o $(BINARY)-gui ./cmd/topswatch-gui

test: ## Run tests
	go test ./...

lint: ## Run linters
	golangci-lint run ./...

run: build ## Build and run with default config
	./$(BINARY) --config topswatch.yaml

text: build ## One-shot text output
	./$(BINARY) --config topswatch.yaml --text

tui: build ## Terminal dashboard attached to the local daemon
	./$(BINARY) --tui

docker: ## Build Docker image
	docker build --network=host -t $(IMAGE):$(TAG) .

clean: ## Remove build artifacts
	rm -f $(BINARY) $(BINARY)-gui
