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
PLATFORMS   := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

# Docker Hub coordinates. Override to publish somewhere else:
#   make docker-buildx IMAGE=ghcr.io/you/polyemesis PUSH=1
IMAGE            ?= rainmanjam/polyemesis
# linux/amd64 covers Intel and AMD servers plus Windows via WSL2; linux/arm64
# covers Apple Silicon under Docker Desktop, Ampere/Graviton servers and the
# Raspberry Pi 4/5. Those two are the whole practical matrix — there is no
# darwin or windows Docker image, because Docker Desktop runs a Linux VM.
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

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
test: preflight-guard ## Run the Go test suite
	go test ./...

.PHONY: preflight-guard
preflight-guard: ## Prove the route-coverage preflight survives -run, -skip and -count=0
	@# internal/api runs a preflight before whatever the caller selected, so that a
	@# test filter cannot switch the route coverage ledger off. That guard is only
	@# worth anything while it is WIRED, and a TestMain is a quiet thing to delete.
	@#
	@# So its liveness is proven the same way everything else in the ledger is
	@# proven: by running something that fails if it stopped.
	@#
	@# THREE switches, because the first version of this target probed one. Go has
	@# three ways to decide that a test does not run, and TestMain overrode only
	@# -run: `-skip TestLedgerPreflight` reported ok in 41.7s against a registry
	@# that failed seven ways, and `-count=0` ran nothing at all in 0.2s. A guard
	@# thorough over a set that excludes the bypass is the exact shape the ledger
	@# exists to catch, so this target now probes each switch by name and says
	@# which one defeated it.
	@#
	@# CI RUNS THIS TARGET. It used to be reachable only through `make test`, and
	@# ci.yml invokes `go test` directly -- so the gate existed on developer
	@# machines and nowhere else, which is a local-only gate described as a gate.
	@# -count=1 comes FIRST so the probe wins when the probe is itself a -count:
	@# Go's flag package takes the last occurrence, and `-count=0 -count=1` would
	@# have quietly turned the -count=0 probe into a plain run that passes.
	@for probe in '-run XXXNoSuchTest' '-skip TestLedgerPreflight' '-count=0'; do \
		go test -count=1 ./internal/api $$probe -v 2>&1 \
			| grep -q 'polyemesis: route-coverage preflight ran' \
			|| { echo "THE ROUTE COVERAGE PREFLIGHT IS DEFEATED BY: $$probe"; \
			     echo 'internal/api/main_test.go forces ^TestLedgerPreflight$$ through a'; \
			     echo 'first m.Run with the caller'"'"'s -run, -skip and -count set aside,'; \
			     echo 'so that no filter can leave the ledger unchecked. Under the switch'; \
			     echo 'above its marker no longer prints, which means TestMain was removed,'; \
			     echo 'renamed, or stopped neutralising that switch. Restore it; do not'; \
			     echo 'delete this check, and do not narrow the probe list.'; \
			     exit 1; }; \
	done
	@echo 'preflight-guard: the route-coverage preflight still runs under -run, -skip and -count=0'

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

.PHONY: fmtcheck
fmtcheck: ## Fail if any Go source needs formatting (what CI actually gates on)
	@out="$$(gofmt -l ./cmd ./internal)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: $(UI_DIR)/node_modules ## Lint the frontend
	cd $(UI_DIR) && npm run lint

# CONTRIBUTING.md and the PR template both told contributors to run `make lint`,
# which did not exist -- and `check` claimed to be "everything CI would run"
# while omitting both the lint and the gofmt gate that CI fails on. Adding the
# two targets was the honest fix: the instruction was reasonable, the Makefile
# was the thing that was wrong.
.PHONY: check
check: fmtcheck vet test typecheck lint ## Everything CI would run

# ----------------------------------------------------------------- release

.PHONY: release
release: ui ## Cross-compile release binaries into dist/
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out=dist/$(BINARY)-$(VERSION)-$$os-$$arch; \
	  if [ "$$os" = windows ]; then out=$$out.exe; fi; \
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

# The GPU images are separate tags rather than one fat image because NVENC's
# runtime arrives from the host at `docker run`, not from the build — see
# docs/HARDWARE.md. Neither has been built end to end on a machine with a GPU.
.PHONY: docker-cuda
docker-cuda: ## Build the NVIDIA/NVENC image (needs nvidia-container-toolkit at run time)
	docker build -f Dockerfile.cuda -t polyemesis:$(VERSION)-cuda -t polyemesis:cuda .

.PHONY: docker-vaapi
docker-vaapi: ## Build the Intel/AMD VA-API image (needs --device /dev/dri at run time)
	docker build -f Dockerfile.vaapi -t polyemesis:$(VERSION)-vaapi -t polyemesis:vaapi .

# Multi-architecture publish. Defaults to BUILD ONLY: a bare `make docker-buildx`
# proves both architectures compile and pushes nothing, which is what you want
# in CI on a pull request. Publishing is opt-in with PUSH=1.
#
# buildx cannot `--load` a multi-arch result into the local docker image store
# (the store holds one architecture per tag), so the no-push build discards its
# output. That is fine: the point of the no-push run is that it FAILS if either
# architecture cannot build.
.PHONY: docker-buildx
docker-buildx: ## Build $(IMAGE) for $(DOCKER_PLATFORMS). PUSH=1 to publish.
	@docker buildx inspect polyemesis-builder >/dev/null 2>&1 \
	  || docker buildx create --name polyemesis-builder --driver docker-container >/dev/null
	docker buildx build --builder polyemesis-builder \
	  --platform $(DOCKER_PLATFORMS) \
	  --build-arg VERSION=$(VERSION) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest \
	  $(if $(PUSH),--push,) .
	@if [ -z "$(PUSH)" ]; then echo "  built for $(DOCKER_PLATFORMS), pushed nothing (PUSH=1 to publish)"; fi

# ------------------------------------------------------------------- clean

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BINARY) dist coverage.out
	rm -rf $(UI_OUT)/assets $(UI_OUT)/favicon.svg
	git checkout -- $(UI_OUT)/index.html 2>/dev/null || true

.PHONY: clean-all
clean-all: clean ## Also remove node_modules and the local data directory
	rm -rf $(UI_DIR)/node_modules data
