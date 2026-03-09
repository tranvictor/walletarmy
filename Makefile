.PHONY: setup test lint vet check build clean

# Setup git hooks for local development
setup:
	@echo "Setting up git hooks..."
	@chmod +x .githooks/*
	@git config core.hooksPath .githooks
	@echo "✓ Git hooks configured"

# Run all tests
test:
	go test -race -coverprofile=coverage.out ./...
	@echo "Coverage report: go tool cover -html=coverage.out"

# Run linter (requires golangci-lint)
lint:
	golangci-lint run --timeout=5m

# Run go vet
vet:
	go vet ./...

# Run all checks (vet + test + lint)
check: vet test lint
	@echo "✓ All checks passed"

# Build all packages
build:
	go build -v ./...

# Clean generated files
clean:
	rm -f coverage.out coverage.html
	go clean ./...

# Show help
help:
	@echo "Available targets:"
	@echo "  setup  - Configure git hooks for pre-push checks"
	@echo "  test   - Run all tests with race detection"
	@echo "  lint   - Run golangci-lint"
	@echo "  vet    - Run go vet"
	@echo "  check  - Run all checks (vet, test, lint)"
	@echo "  build  - Build all packages"
	@echo "  clean  - Remove generated files"
