# polyemesis
#
# `make build` is the only target most people need: it compiles the React UI,
# embeds it, and produces a single self-contained binary.

BINARY      := polyemesis
CMD         := ./cmd/polyemesis
UI_DIR      := ui
UI_OUT      := internal/web/dist
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Cross-compilation targets for `make release`.
PLATFORMS   := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------- ui

.PHONY: ui-deps
ui-deps: ## Install frontend dependencies
	cd $(UI_DIR) && npm ci 2>/dev/null || (cd $(UI_DIR) && npm install)

$(UI_DIR)/node_modules:
	$(MAKE) ui-deps

.PHONY: ui
ui: $(UI_DIR)/node_modules ## Build the web UI into internal/web/dist
	cd $(UI_DIR) && npm run build

.PHONY: ui-dev
ui-dev: $(UI_DIR)/node_modules ## Run the Vite dev server against a local backend on :8080
	cd $(UI_DIR) && npm run dev

# ------------------------------------------------------------------- build

.PHONY: build
build: ui ## Build the single binary with the UI embedded
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)
	@echo
	@echo "  built ./$(BINARY)  ($(VERSION))"
	@ls -lh $(BINARY) | awk '{print "  size:", $$5}'
	@echo

.PHONY: build-go
build-go: ## Build the binary without rebuilding the UI
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)

.PHONY: run
run: build ## Build and run with a local ./data directory
	./$(BINARY) -data ./data

.PHONY: dev
dev: build-go ## Run the backend only (pair with `make ui-dev`)
	./$(BINARY) -data ./data -log debug

# -------------------------------------------------------------------- test

.PHONY: test
test: ## Run the Go test suite
	go test ./...

.PHONY: test-v
test-v: ## Run the Go test suite verbosely
	go test -v ./...

.PHONY: cover
cover: ## Run tests with a coverage summary
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: typecheck
typecheck: $(UI_DIR)/node_modules ## Typecheck the frontend without emitting
	cd $(UI_DIR) && npx tsc -b --noEmit

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format Go sources
	gofmt -w ./cmd ./internal

.PHONY: check
check: vet test typecheck ## Everything CI would run

# ----------------------------------------------------------------- release

.PHONY: release
release: ui ## Cross-compile release binaries into dist/
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out=dist/$(BINARY)-$(VERSION)-$$os-$$arch; \
	  [ "$$os" = windows ] && out=$$out.exe; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    go build -trimpath -ldflags '$(LDFLAGS)' -o $$out $(CMD) || exit 1; \
	done
	@echo
	@ls -lh dist/

# ------------------------------------------------------------------ docker

.PHONY: docker
docker: ## Build the Docker image (optional; Docker is never required)
	docker build -t polyemesis:$(VERSION) -t polyemesis:latest .

# ------------------------------------------------------------------- clean

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BINARY) dist coverage.out
	rm -rf $(UI_OUT)/assets $(UI_OUT)/favicon.svg
	git checkout -- $(UI_OUT)/index.html 2>/dev/null || true

.PHONY: clean-all
clean-all: clean ## Also remove node_modules and the local data directory
	rm -rf $(UI_DIR)/node_modules data
