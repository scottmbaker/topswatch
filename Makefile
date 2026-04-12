BINARY := topswatch
IMAGE  := topswatch
TAG    ?= latest

.PHONY: build test lint clean docker run help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build the binary
	go build -o $(BINARY) ./cmd/topswatch

test: ## Run tests
	go test ./...

lint: ## Run linters
	golangci-lint run ./...

run: build ## Build and run with default config
	./$(BINARY) --config topswatch.yaml

text: build ## One-shot text output
	./$(BINARY) --config topswatch.yaml --text

docker: ## Build Docker image
	docker build --network=host -t $(IMAGE):$(TAG) .

clean: ## Remove build artifacts
	rm -f $(BINARY)
