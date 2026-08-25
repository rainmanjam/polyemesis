#!/usr/bin/env bash
# lib-preflight.sh -- refuse to run rather than report on a missing tool.
#
# On 2026-08-25 five acceptance suites ran on a host with /usr/local/go/bin
# off PATH:
#
#   acceptance-automod     0 passed, 12 failed
#   acceptance-oauth       0 passed, 44 failed
#   acceptance-hooks       0 passed, 29 failed
#   acceptance-transcribe  0 passed,  8 failed
#   acceptance-chat        1 passed,  9 failed
#
# Read cold, that says the product fails over a hundred assertions. It
# actually says `go run` printed "go: command not found" to stderr, that text
# got captured as if it were driver output, and every check that parsed a
# field out of it found nothing and reported FAIL. The only tell was that
# every run finished in 0-3 seconds -- nothing in the tally itself
# distinguished "the product regressed" from "the harness never ran".
#
# scripts/acceptance-failover.sh already had the right instinct for its own
# binary: build it here rather than assume it exists, and treat the failure
# as fatal. This library gives every suite that same device for whatever it
# actually depends on -- go, ffmpeg, ffprobe, docker, openssl, curl, node,
# python3, a prebuilt binary -- and makes the refusal impossible to confuse
# with an assertion failure:
#
#   * the check runs BEFORE any assertion, so a suite with a missing
#     prerequisite prints nothing that looks like a pass/fail tally, and
#   * it exits POLY_PREFLIGHT_EXIT (3), a code no suite here uses for a real
#     assertion failure (that is always 0 or 1), so a runner or a human can
#     tell "could not run" from "ran and failed" from the exit status alone,
#     without reading the log.
#
# Source this after computing SCRIPTS/ROOT and before touching go, ffmpeg,
# docker, or any binary this suite builds or shells out to:
#
#   . "$SCRIPTS/lib-preflight.sh"
#   poly_require_cmd go "needed to build/run the acceptance driver"
#   poly_require_cmd ffmpeg
#   poly_require_exec "$BIN" "build first: make build"

# 0 and 1 already mean "passed" and "failed" throughout these suites. 3 means
# something different: this run measured nothing, in either direction.
POLY_PREFLIGHT_EXIT=3

# poly_preflight_fail <message...>
# Every helper below funnels through this so the message shape and exit code
# are identical across all suites, which is what makes them scriptable.
poly_preflight_fail() {
  echo "CANNOT RUN: $* -- this suite has nothing to say about the product without it" >&2
  exit "$POLY_PREFLIGHT_EXIT"
}

# poly_require_cmd <command> [why]
#
# Deliberately does not stop at `command -v`: on at least one shell this was
# tested against, `command -v` reports a plain, non-executable regular file
# sitting on PATH as found. A stub left behind by a half-finished install (or
# a broken symlink, or a file that lost its +x bit) would sail through that
# check and fail confusingly deep inside the suite instead of here. So this
# also resolves the path and checks -x on it directly.
poly_require_cmd() {
  local cmd="$1" why="${2:-}" p
  p="$(command -v "$cmd" 2>/dev/null)"
  [ -n "$p" ] && [ -x "$p" ] || \
    poly_preflight_fail "'$cmd' is not on PATH${why:+ ($why)}"
}

# poly_require_exec <path> [why]
# For a binary this suite expects to already be built (most suites run
# `make build` first and only check for the result).
poly_require_exec() {
  local path="$1" why="${2:-build first: make build}"
  [ -x "$path" ] || poly_preflight_fail "$path is not built ($why)"
}

# poly_require_docker [why]
poly_require_docker() {
  local why="${1:-}"
  poly_require_cmd docker "$why"
  docker info >/dev/null 2>&1 || \
    poly_preflight_fail "docker daemon is not reachable${why:+ ($why)}"
}

# poly_require_build <bin> <pkg> [label]
# For a suite that builds its own binary rather than assuming one (the
# failover device): requires go, then builds. A failed build is a preflight
# failure, not an assertion failure -- a suite that cannot build the thing it
# measures has nothing to say about it.
poly_require_build() {
  local bin="$1" pkg="$2" label="${3:-$pkg}"
  poly_require_cmd go "needed to build $label"
  go build -o "$bin" "$pkg" || poly_preflight_fail "could not build $label"
}

# poly_require_writable_dir <dir>
# For a workdir this suite is about to rm -rf/mkdir -p/cd into and then run a
# server against. A parent that is read-only or missing fails mkdir with a
# generic message; this names the actual problem before anything else runs.
poly_require_writable_dir() {
  local dir="$1" parent
  parent="$(dirname "$dir")"
  [ -d "$parent" ] && [ -w "$parent" ] || \
    poly_preflight_fail "$parent is not a writable directory (workdir: $dir)"
}
