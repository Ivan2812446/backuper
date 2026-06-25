# Makefile for backuper
#
# Pure-Go project (modernc.org/sqlite, lumberjack) — CGO is NOT required.
# Binaries are static, no runtime dependencies, timezones embedded (time/tzdata).
#
# If `go` is not in PATH, the toolchain from GOROOT is used. Override with:
#   make GOROOT=/path/to/go-sdk
GOROOT ?= /home/ivan/go-sdk

# Use `go` from PATH if available, otherwise fall back to $(GOROOT)/bin/go.
GO := $(shell command -v go 2>/dev/null || echo $(GOROOT)/bin/go)

BIN_DIR    := bin
SERVER_PKG := ./cmd/server
CLIENT_PKG := ./cmd/client

# Certificate output directory and hosts (override on the command line).
CERTS_DIR   ?= certs
CLIENT_HOST ?= 192.168.1.50
SERVER_HOST ?= 192.168.1.10

.PHONY: all build build-windows vet tidy test cover certs test-all clean

all: build

# Build Linux server and client into ./bin
build:
	$(GO) build -o $(BIN_DIR)/backuper-server $(SERVER_PKG)
	$(GO) build -o $(BIN_DIR)/backuper-client $(CLIENT_PKG)

# Cross-compile Windows .exe binaries into ./bin
build-windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BIN_DIR)/backuper-server.exe $(SERVER_PKG)
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BIN_DIR)/backuper-client.exe $(CLIENT_PKG)

# Run go vet on all packages
vet:
	$(GO) vet ./...

# Tidy module dependencies
tidy:
	$(GO) mod tidy

# Go unit + in-process integration tests with the race detector
test:
	$(GO) test ./... -race -count=1

# Test coverage summary
cover:
	$(GO) test ./... -cover

# Generate TLS certificates via the server's gen-certs subcommand.
# Builds the server first so the subcommand is available.
certs: build
	$(BIN_DIR)/backuper-server gen-certs -out $(CERTS_DIR) -client-host $(CLIENT_HOST) -server-host $(SERVER_HOST)

# Run the project test script
test-all:
	scripts/test-all

# Remove built binaries
clean:
	rm -rf $(BIN_DIR)
