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

# 657 vitest tests that no local target ran. They cover the pure logic the
# browser suite cannot enumerate -- platform link construction is five
# platforms times several missing-field cases -- and they are the cheapest
# thing in CI, so the only reason they were not in `check` was that nobody
# noticed they were not in `check`.
.PHONY: ui-test
ui-test: $(UI_DIR)/node_modules ## Run the frontend unit tests (vitest)
	cd $(UI_DIR) && npm test

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

# WHAT CI RUNS, VERBATIM, and it did not used to be.
#
# This target ran `go test ./...`. CI runs
# `POLYEMESIS_LEDGER=strict go test -race -timeout 20m ./...`, and the two
# differ in ways that decide whether a class of defect can be seen locally AT
# ALL:
#
#   POLYEMESIS_LEDGER=strict  makes internal/api's route-coverage ledger run its
#                             counterpart proofs, and the LiveTools shape
#                             inspectors with them. Without it those never
#                             executed on any developer machine -- the ledger's
#                             whole claim is that an excuse cannot discharge on
#                             a test NAME, and that claim was only ever tested
#                             in CI.
#   -race                     the engine reconciles from several goroutines. A
#                             data race here is a stream that stops for one
#                             viewer, and nothing local looked for one.
#
# The parity guard certified this target as CI-equivalent for as long as it was
# not, because it matched the substring `go test` and stopped there. A guard
# that reads a command NAME and not its flags is the same mistake as a ledger
# that reads a test name: internal/testenv/checkparity_invocation_test.go now
# compares the environment and the flags.
#
# WHAT IT COSTS, out loud. `make check` was about four minutes and is now about
# twenty-five, because the race detector multiplies the largest suite in the
# tree. That is the real price of the gate telling the truth; `go test ./...`
# is still one command away for anybody who wants the fast, weaker answer and
# knows that is what they are getting. -race also needs a C toolchain, so this
# target now fails on a machine with Go and no cc, where it used to pass.
.PHONY: test
test: preflight-guard ## Run the Go suite the fast way -- the loop you run before every commit
	go test ./...

test-ci: preflight-guard ## Run the Go suite EXACTLY as CI does: strict ledger, -race, 20m
	POLYEMESIS_LEDGER=strict go test -race -timeout 20m ./...

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

.PHONY: coverage-instrument-guard
coverage-instrument-guard: ## Prove -cover on internal/api measures the selection, not the preflight
	@# The other half of the same mechanism. preflight-guard above proves the
	@# forced preflight still RUNS; this proves it is not the only thing the
	@# coverage profile ever sees. #217: with the forced pass placed first, the
	@# profile was written on its way out and zero tests, one test and the whole
	@# suite all reported 22.0%. Runs the probes rather than reading the source.
	@./scripts/coverage-instrument-guard.sh

.PHONY: termination-guard
termination-guard: ## Prove no script kills a process and then acts as though it had
	@# The shell half of the #179/#180 class. The workflow guard
	@# (internal/testenv/workflowtimeout_test.go) obliges a step timeout and
	@# nothing else; this obliges the observation itself -- a kill followed by a
	@# sleep, a verdict or a read with nothing looking again in between, a bare
	@# `wait`, or a `$$!` captured from a pipeline where it names the wrong
	@# process. Its own red/green fixtures live in scripts/test-termination-guard.sh.
	@./scripts/termination-guard.sh

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

# BOTH VETS, because CI runs both. The Windows leg catches a _windows.go file
# that no build on this machine compiles -- internal/supervisor has one, and a
# vet failure there costs the whole `go build, vet, test` job and a round trip.
# Seconds; it type-checks, it does not build.
.PHONY: vet
vet: ## Run go vet, for this platform and for the Windows build
	go vet ./...
	GOOS=windows go vet ./...

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

# THE SCRIPTS NOTHING RUNS STILL HAVE TO PARSE.
#
# 44 files under scripts/, and 25 of them are acceptance suites that run only in
# the acceptance matrix, on dispatch, or not at all.
# scripts/acceptance-multistream.sh is 70 KB, runs NOWHERE in CI on purpose --
# with credentials it publishes to a real account, which docs/TESTING.md
# explains -- and three Go tests nonetheless cite its behaviour as the thing
# they are keeping in step with. Nothing parsed it. A syntax error would have
# been found by whoever next ran it by hand, on a schedule nobody controls.
#
# `bash -n` parses without executing: no FFmpeg, no ports, no credentials, no
# network, under a second for the whole directory. It cannot say a script is
# CORRECT, only that it is not garbage -- which is the class of breakage an
# unrun script accumulates, because nothing else looks at it at all.
#
# GLOBBED, NOT LISTED. release.yml already did this for install.sh and
# acceptance-install.sh by name, and naming scripts is how you cover the two
# somebody remembered.
.PHONY: sh-syntax
sh-syntax: ## bash -n every scripts/*.sh, including the ones nothing runs
	@n=0; for f in scripts/*.sh scripts/*/*.sh; do \
	  [ -e "$$f" ] || continue; \
	  bash -n "$$f" || exit 1; \
	  n=$$((n+1)); \
	done; \
	if [ "$$n" -lt 20 ]; then \
	  echo "sh-syntax: only $$n scripts parsed; the glob has gone stale and this target is passing having checked almost nothing"; \
	  exit 1; \
	fi; \
	echo "sh-syntax: $$n shell scripts parse"

# ------------------------------------------------------------------- gate

# WHICH WORKTREE AM I IN. This repository has seventeen git worktrees, and a
# green `go test ./...` in the wrong one is BYTE-IDENTICAL to a green run in
# the right one -- there is nothing in the output to notice. That is how a full
# suite got run in the primary checkout today and its result reported for a
# branch that lived in a temp worktree and had never been tested at all.
#
# Two rungs, because the cheap one has to be free or it gets skipped:
#
#   WARNING  the banner below, always on, three git calls, tells you after the
#            fact what you actually just tested.
#   CONTROL  `make check BRANCH=some-branch` refuses to run unless the branch
#            matches, and names both. Use it the moment you are about to type a
#            branch name into a report or a PR comment.
#
# BRANCH is deliberately NOT mandatory: most people have one worktree and would
# come to resent typing it, and a device people resent is a device people
# route around.
WORKTREE   := $(shell git rev-parse --show-toplevel 2>/dev/null || pwd)
ON_BRANCH  := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '(not a git checkout)')
TREE_STATE := $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo dirty || echo clean)

# Printed while the makefile is being READ, not from a recipe, so it is the
# first thing on the terminal no matter what `check` ends up building and no
# matter what -j does to the order of the prerequisites below.
ifneq ($(filter check check-browser check-full,$(MAKECMDGOALS)),)
$(info )
$(info   worktree   $(WORKTREE))
$(info   branch     $(ON_BRANCH) ($(TREE_STATE)))
$(info )
endif

# Also parse-time, so a mismatch costs nothing: make stops before it has run a
# single recipe, rather than after twelve minutes of tests on the wrong tree.
#
# COMMAND LINE ONLY, and that is not fussiness. make imports the environment as
# make variables, and BRANCH is a common enough name that a shell prompt or a
# CI runner exporting it would make EVERY target here fail on a tree that is
# perfectly fine -- a device whose own false alarms teach people to work around
# it. `make check BRANCH=x` is a statement of intent; `export BRANCH=x` in some
# other tool's dotfile is not.
ifeq ($(origin BRANCH),command line)
ifneq ($(BRANCH),)
ifneq ($(BRANCH),$(ON_BRANCH))
$(error you asked for BRANCH=$(BRANCH), but $(WORKTREE) is on $(ON_BRANCH) -- refusing to run. `git worktree list` will show you where $(BRANCH) is checked out)
endif
endif
endif

# CONTRIBUTING.md and the PR template both told contributors to run `make lint`,
# which did not exist -- and `check` claimed to be "everything CI would run"
# while omitting both the lint and the gofmt gate that CI fails on. Adding the
# two targets was the honest fix: the instruction was reasonable, the Makefile
# was the thing that was wrong.
#
# AND THEN IT LIED AGAIN, about different targets. CI's "ui typecheck, lint,
# build" job runs five commands; `check` ran two of them, so 657 vitest tests
# and the production build -- the step that catches a type error tsc --noEmit
# does not, because `npm run build` runs `tsc -b` for real and then bundles --
# were gated in CI and nowhere local. A NAME IS NOT A DEVICE: "everything CI
# would run" was true for about as long as it took someone to add a CI step.
# The description below therefore lists what runs instead of claiming a
# property, so that it goes stale visibly.
#
# Still deliberately absent: `npm audit --audit-level=high`, which CI runs and
# which fails on an advisory published overnight against code you did not
# touch. That belongs on a gate you can rerun, not between you and a commit.
#
# `ui` rebuilds internal/web/dist, so `check` leaves build output in the tree.
# That is what CI gates on and `make clean` puts it back.
# AND THE GO HALF FELL BEHIND THE SAME WAY, which is the third time. CI's `go
# build, vet, test` job grew `make coverage-instrument-guard` -- the probe that
# proves `-cover` on internal/api measures the selection and not the forced
# preflight, the #217 defect where zero tests, one test and the whole suite all
# reported 22.0% -- and nothing reachable from `check` ran it. The parity guard
# did not notice because it only ever compared the `ui` job.
.PHONY: check
# TWO TARGETS, BECAUSE ONE CANNOT BE BOTH FAST AND CI.
#
# `check` was made to run CI's exact go test -- strict ledger, -race, 20m --
# because its parity guard was certifying a parity it did not have. That closed
# the false claim and bought a four-minute loop turning into a twenty-five
# minute one, which is how a pre-commit check stops being run at all. A gate
# people route around is back to rung zero, which is where this started.
#
# So: `check` is the loop, `check-ci` is the parity, and the guard in
# internal/testenv/checkparity_invocation_test.go compares CHECK-CI against
# ci.yml. Neither target claims to be the other. `check`'s help line says
# "fast", so nobody reads it as a promise about CI.
check: fmtcheck vet test coverage-instrument-guard sh-syntax typecheck lint ui-test ui ## FAST pre-commit loop: gofmt, both vets, plain go test, coverage probe, bash -n, tsc, oxlint, vitest, ui build

check-ci: fmtcheck vet test-ci coverage-instrument-guard sh-syntax typecheck lint ui-test ui ## Everything `check` runs, but with CI's exact go test (strict ledger, -race). Slow; needs a C toolchain

# NOT IN `check`, ON PURPOSE. Everything above runs on a bare checkout with Go
# and Node and nothing else, and that property is worth keeping: the moment the
# gate you run before every commit needs a daemon installed, it becomes the
# gate you stop running. This one builds an image and drives Playwright against
# it, ~4 minutes.
#
# It earns a target anyway because it caught a real defect three times in one
# day and every one of those catches happened in CI, hours after the push --
# the suite was in no local gate at all, so the first person to hear about the
# break was always a red check.
.PHONY: check-browser
check-browser: ## Playwright against the real container image (~4 min, needs Docker)
	./scripts/acceptance-browser.sh

.PHONY: check-full
check-full: check check-browser ## check plus the browser suite (needs Docker)

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
