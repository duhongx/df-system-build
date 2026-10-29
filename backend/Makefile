.PHONY: build run test test-local clean dev

# Build the binary
build:
	bash ../scripts/build_dfops.sh

# Run in development mode (auto-creates required dirs)
run: init
	go run ./cmd/server

# Run with hot reload (requires air)
dev:
	air

# Run tests
test:
	go test ./... -v

# Full local checks with an isolated, password-authenticated TLS PostgreSQL.
test-local:
	python3 ../scripts/check_local.py

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
