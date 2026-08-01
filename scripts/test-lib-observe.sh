#!/usr/bin/env bash
# Tests for lib-observe.sh -- the helpers that make a timed-out wait explain
# itself.
#
# WHY A TEST FOR TEST CODE
#
# lib-observe.sh exists because issue #38's two failures asserted causes nobody
# had measured. A diagnostic that is itself wrong is worse than none: it does
# not merely fail to help, it points the next reader at the wrong component with
# the full authority of a printed conclusion. The claim that earns the most
# trust -- "the ceiling is too low; this is not a product failure" -- is exactly
# the one that must never fire on a genuine product failure.
#
# So the case that matters most here is the NEGATIVE one: case A proves a value
# that never arrives does NOT get excused as a timing problem. Cases B and C
# prove the excuse still fires when it is true. A change that broke either
# direction would otherwise be invisible until it misdirected somebody.
#
# Usage:  ./scripts/test-lib-observe.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-observe.sh"

# Fast poll: these tests assert on ordering and content, never on duration.
POLY_POLL_INTERVAL=0.05

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
C="$WORK/counter"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

# The sampler runs inside a command substitution, so it executes in a SUBSHELL
# and cannot carry state in a shell variable -- an assignment there is discarded
# with the subshell. Real samplers read external state (docker, an HTTP status
# route) and are unaffected; these fakes have to use a file for the same reason.
tick()  { local n; n=$(cat "$C" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$C"; echo "$n"; }
reset() { echo 0 > "$C"; }

# A ceiling of 0 admits exactly one in-window sample, which makes the grace
# sample tick #2 and the "one sample late" case exactly expressible.
never()      { printf 'absent'; }
late()       { [ "$(tick)" -ge 2 ] && printf 'running/ready'    || printf 'created/starting'; }
line_never() { printf 'slate 1 false 0'; }
line_late()  { [ "$(tick)" -ge 2 ] && printf 'primary 1 true 0' || printf 'slate 1 false 0'; }
flapping()   { [ $(( $(tick) % 2 )) -eq 0 ] && printf 'up' || printf 'down'; }
quick()      { [ "$(tick)" -ge 2 ] && printf 'running/ready'    || printf 'created/starting'; }

CEILING_CLAIM="ceiling is too low"

step "1. A value that never arrives is not excused as a timing problem"
reset; poly_poll_until "the broker" running/ready 0 never > "$WORK/a" 2>&1
rc=$?
[ "$rc" -eq 1 ] && ok "a wait that never matched returns 1" \
                || bad "expected rc=1, got $rc"
grep -q "$CEILING_CLAIM" "$WORK/a" \
  && bad "a value that never arrived was blamed on the ceiling" \
  || ok "no ceiling excuse for a value that never arrived"
grep -q "still: absent" "$WORK/a" \
  && ok "the post-ceiling sample is reported as unchanged" \
  || bad "the post-ceiling sample was not reported"

step "2. A value that arrives one sample late says so"
reset; poly_poll_until "the broker" running/ready 0 late > "$WORK/b" 2>&1
grep -q "$CEILING_CLAIM" "$WORK/b" \
  && ok "a late arrival is attributed to the ceiling, not the product" \
  || bad "a late arrival was not distinguished from a failure"

step "3. The same distinction holds for a field within a status line"
reset; poly_poll_field "the active feed" 1 primary 0 line_never > "$WORK/c" 2>&1
grep -q "$CEILING_CLAIM" "$WORK/c" \
  && bad "poll_field blamed the ceiling for a field that never changed" \
  || ok "poll_field does not excuse a field that never changed"

reset; poly_poll_field "the active feed" 1 primary 0 line_late > "$WORK/d" 2>&1
grep -q "$CEILING_CLAIM" "$WORK/d" \
  && ok "poll_field attributes a late field to the ceiling" \
  || bad "poll_field missed a late field"

step "4. The trajectory records the whole line, not just the matched field"
grep -q "slate 1 false 0" "$WORK/c" \
  && ok "the unwatched fields survive into the report" \
  || bad "the report lost the fields nobody was waiting on"

step "5. A wait that succeeds says nothing at all"
reset; poly_poll_until "the broker" running/ready 5 quick > "$WORK/e" 2>&1
rc=$?
[ "$rc" -eq 0 ] && ok "a satisfied wait returns 0" || bad "expected rc=0, got $rc"
[ -s "$WORK/e" ] \
  && bad "a satisfied wait printed $(wc -l < "$WORK/e") line(s); it must be silent" \
  || ok "a satisfied wait is silent"

step "6. An oscillating value is bounded, and admits what it dropped"
reset; POLY_TRAJ_MAX_RUNS=4 poly_poll_until "the broker" never-matches 1 flapping \
  > "$WORK/f" 2>&1
runs=$(grep -c "^          t+" "$WORK/f")
[ "$runs" -le 4 ] \
  && ok "the trajectory is capped at POLY_TRAJ_MAX_RUNS ($runs shown)" \
  || bad "the cap did not hold: $runs runs printed"
grep -q "further run(s) elided" "$WORK/f" \
  && ok "the report admits how many runs it dropped" \
  || bad "runs were dropped silently -- the same disease as a reflexive rerun"

step "7. Run-length encoding collapses a stable signal"
reset; poly_poll_until "the broker" running/ready 1 never > "$WORK/g" 2>&1
grep -q "observed 1 distinct run(s)" "$WORK/g" \
  && ok "a signal that never changed reports one run, not one line per sample" \
  || bad "the trajectory did not collapse a stable signal"

total=$((pass + fail))
EXPECTED_CHECKS=12
printf "\n"
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -gt 0 ]; then
  printf "  \033[31mlib-observe: %d of %d checks FAILED\033[0m\n\n" "$fail" "$total"
  exit 1
fi
printf "  \033[32mlib-observe: %d checks passed\033[0m\n\n" "$total"
