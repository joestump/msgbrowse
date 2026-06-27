# msgbrowse Makefile
#
# Common targets:
#   make build     build the msgbrowse binary into ./bin
#   make test      run the test suite
#   make check     gofmt + go vet + tests (CI gate)
#   make up            bring up the Docker compose stack
#   make signal-import import the signal-export archive (in the container)
#   make embed         compute embeddings for new messages (in the container)
#   make journal       rebuild the journal (mechanical + digests)

BINARY      := msgbrowse
PKG         := github.com/joestump/msgbrowse
BIN_DIR     := bin
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X $(PKG)/internal/cli.Version=$(VERSION) \
               -X $(PKG)/internal/cli.Commit=$(COMMIT) \
               -X $(PKG)/internal/cli.BuildDate=$(BUILD_DATE)

GO          ?= go

# The SQLite driver (mattn/go-sqlite3) needs cgo and the sqlite_fts5 build tag
# to enable the FTS5 full-text search extension used by keyword search.
TAGS        := sqlite_fts5
export CGO_ENABLED = 1

.PHONY: all build run test cover check fmt fmt-check vet tidy clean up up-bundled down logs signal-import embed journal

all: check build

build: ## Build the binary
	$(GO) build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/msgbrowse

run: build ## Build then run the web UI
	$(BIN_DIR)/$(BINARY) serve

test: ## Run all tests
	$(GO) test -tags "$(TAGS)" ./...

cover: ## Run tests with coverage
	$(GO) test -tags "$(TAGS)" -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

fmt: ## Format the code
	$(GO) fmt ./...

fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet: ## Run go vet
	$(GO) vet -tags "$(TAGS)" ./...

tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

check: fmt-check vet test ## CI gate: format check, vet, tests

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

up: ## Start msgbrowse (points at your external LiteLLM via .env)
	docker compose up -d --build

up-bundled: ## Start msgbrowse + the bundled LiteLLM proxy
	docker compose --profile bundled-llm up -d --build

down: ## Stop the Docker compose stack
	docker compose --profile bundled-llm down

logs: ## Tail the msgbrowse container logs
	docker compose logs -f msgbrowse

signal-import: ## Import the signal-export archive (in the container)
	docker compose run --rm msgbrowse signal-import

embed: ## Compute embeddings for new messages (in the container)
	docker compose run --rm msgbrowse embed

journal: ## Rebuild the journal in the container
	docker compose run --rm msgbrowse journal
