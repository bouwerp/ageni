.PHONY: help build test lint clean install run fmt vet ci

help:
	@echo "Available targets:"
	@echo "  build      - Build the ageni binary"
	@echo "  test       - Run tests with coverage"
	@echo "  lint       - Run golangci-lint"
	@echo "  fmt        - Format code with gofmt"
	@echo "  vet        - Run go vet"
	@echo "  clean      - Remove build artifacts"
	@echo "  install    - Install ageni to GOPATH/bin"
	@echo "  run        - Build and run ageni"
	@echo "  ci         - Run all CI checks (fmt + vet + test + lint)"

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

build:
	@echo "Building ageni $(VERSION)..."
	go build -v -ldflags "$(LDFLAGS)" -o ageni ./cmd/ageni

test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "Coverage report:"
	go tool cover -func=coverage.out

lint:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "golangci-lint not found. Install with:"; \
		echo "  brew install golangci-lint"; \
		echo "  or"; \
		echo "  go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

fmt:
	@echo "Formatting code..."
	gofmt -s -w .

vet:
	@echo "Running go vet..."
	go vet ./...

clean:
	@echo "Cleaning..."
	rm -f ageni
	rm -f coverage.out
	rm -rf dist

install:
	@echo "Installing ageni..."
	go install -ldflags "$(LDFLAGS)" ./cmd/ageni

run: build
	./ageni

ci: fmt vet test lint
	@echo "All CI checks passed!"
