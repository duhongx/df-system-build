.PHONY: build run test clean dev

# Build the binary
build:
	go build -o bin/df-build-server ./cmd/server

# Run in development mode (auto-creates required dirs)
run: init
	go run ./cmd/server

# Run with hot reload (requires air)
dev:
	air

# Run tests
test:
	go test ./... -v

# Run property tests
test-property:
	go test ./... -v -run TestProperty

# Clean build artifacts
clean:
	rm -rf bin/ data/ workspaces/ logs/

# Format code
fmt:
	go fmt ./...

# Lint
lint:
	golangci-lint run

# Create runtime directories
init:
	mkdir -p data workspaces logs
