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

# ----------------------------------------------------------------- the verdict
#
# On 2026-08-26 a browser run whose own output said "BROWSER ACCEPTANCE FAILED"
# was reported as "exit code 0". Twice, in one session. Nothing was wrong with
# the suite. The run had been piped somewhere to keep the log short --
#
#   ./scripts/acceptance-browser.sh 2>&1 | tail -80; echo $?
#
# -- and $? there is TAIL's status, which is 0 whatever the suite did.
# Truncating the output INVERTED the result. That is the most expensive shape a
# check can take: silent, plausible, and green in the one case that had to be
# red. Every other way of losing the answer -- a crash, a kill, a `set -e`
# abort, a preflight bail at exit 3 -- lands the same way, because the reader
# is asking the pipeline rather than the suite.
#
# So the suites stop relying on being asked. Every one of them ends its output
# with exactly one of
#
#   POLY-VERDICT: PASS
#   POLY-VERDICT: FAIL
#
# and the check becomes a POSITIVE assertion for the pass token:
#
#   ./scripts/acceptance-tls.sh 2>&1 | tail -80 | grep -q 'POLY-VERDICT: PASS'
#
# Now truncation, a crash, a kill and a preflight bail all take the token with
# them and read as FAILURE. The direction that used to be dangerous no longer
# exists: to report a pass you have to still have the suite's own word for it.
#
# WHY A TRAP AND NOT A LINE AT THE FOOT OF THE SCRIPT. The foot of the script
# is one exit path out of many, and the ones that skip it are exactly the ones
# worth hearing about -- which is how this class of bug survives being "fixed"
# by convention. Armed as an EXIT trap there is no path left that can skip it.
#
# Arm it on the line after this library is sourced, so the window it does not
# cover is empty:
#
#   . "$SCRIPTS/lib-preflight.sh"
#   trap 'poly_verdict_trap $?' EXIT            # no teardown of its own
#   trap 'poly_verdict_trap $? cleanup' EXIT    # ... or with one
#
# A suite that uses lib-cleanup.sh replaces that with the shared teardown trap,
# `trap 'poly_teardown_trap $? cleanup' EXIT`, which emits the verdict itself
# for the same reason -- see poly_teardown_trap. The handover is deliberate:
# the early arm covers the preflight checks, the teardown trap covers the run.
#
# The one thing that can still lose the token is a LATER `trap ... EXIT` that
# does neither, because bash keeps one EXIT handler and the last one wins. That
# fails toward red -- an absent token reads as FAIL -- so it costs a confusing
# rerun rather than a shipped regression. Do not add one anyway.

# poly_verdict <status>
#
# Derived from the real exit status and from nothing else. A suite that keeps
# its own pass/fail tally can die before it prints the summary, and the tally
# then describes a run that did not finish; the status is the only account of
# the run that exists on every path.
#
# DELIBERATELY `[` and not `[[`, the same choice poly_cleanup_exit documents:
# a non-numeric status makes `[` complain and return non-zero, which lands on
# FAIL, whereas `[[` would evaluate it arithmetically, read it as 0 and print
# PASS. A malformed argument must not be able to manufacture a green run.
poly_verdict() {
  if [ "${1:-1}" -eq 0 ] 2>/dev/null; then
    echo "POLY-VERDICT: PASS"
  else
    echo "POLY-VERDICT: FAIL"
  fi
}

# poly_verdict_trap <status> [teardown-command...] -- the EXIT trap, entire, for
# a suite that does not use lib-cleanup.sh.
#
# `$?` is passed as an ARGUMENT rather than read inside, for the reason
# poly_teardown_trap spells out at length: the shell expands it while assembling
# the call, before any teardown command has had the chance to clobber it. A
# reader who "simplifies" this to read $? in the body turns every red suite
# green and nothing says so.
#
# The teardown runs FIRST so the verdict is the last line of the run's output
# even when teardown prints; and its status is DISCARDED, which is what these
# suites already did with `trap cleanup EXIT` -- an EXIT trap's own status has
# never reached the exit code. This device adds a line of output. It is not
# allowed to change which runs pass.
poly_verdict_trap() {
  local rc="$1"
  shift
  if [ "$#" -gt 0 ]; then
    "$@"
  fi
  poly_verdict "$rc"
  exit "$rc"
}
