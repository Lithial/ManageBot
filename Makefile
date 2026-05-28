GO ?= go
BIN_DIR := bin

.PHONY: all build wrap wrapd fake-claude test test-unit test-integration clean

all: build

build: wrap wrapd fake-claude

wrap:
	$(GO) build -o $(BIN_DIR)/wrap ./cmd/wrap

wrapd:
	$(GO) build -o $(BIN_DIR)/wrapd ./cmd/wrapd

fake-claude:
	$(GO) build -o $(BIN_DIR)/fake-claude ./cmd/fake-claude

test: test-unit

test-unit:
	$(GO) test ./...

test-integration: fake-claude wrapd wrap
	$(GO) test -tags=integration ./test/integration/...

clean:
	rm -rf $(BIN_DIR)
