GO ?= go
BIN_DIR := bin

.PHONY: all build wrap wrapd wrap-mcp fake-claude test test-unit test-integration test-e2e clean

all: build

build: wrap wrapd wrap-mcp fake-claude

wrap:
	$(GO) build -o $(BIN_DIR)/wrap ./cmd/wrap

wrapd:
	$(GO) build -o $(BIN_DIR)/wrapd ./cmd/wrapd

wrap-mcp:
	$(GO) build -o $(BIN_DIR)/wrap-mcp ./cmd/wrap-mcp

fake-claude:
	$(GO) build -o $(BIN_DIR)/fake-claude ./cmd/fake-claude

test: test-unit

test-unit:
	$(GO) test ./...

test-integration: fake-claude wrapd wrap
	$(GO) test -tags=integration ./test/integration/...

# Opt-in real-claude smoke. Needs `claude` on PATH (uses its own auth) and spends
# real API usage; never part of `make test`. Skips if claude is absent.
test-e2e: build
	$(GO) test -tags='integration e2e' ./test/integration/... -run TestE2E -v -timeout 20m

clean:
	rm -rf $(BIN_DIR)
