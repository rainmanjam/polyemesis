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
# Ceiling 2 and a cap of 2, NOT ceiling 1 and a cap of 4, and the difference is
# a test that failed roughly one run in twenty.
#
# poly_poll_until builds its deadline from `date +%s`, which counts whole
# seconds. A ceiling of N therefore buys anywhere between N-1 and N seconds of
# polling, decided by nothing but where in the wall-clock second the call
# happened to land. At ceiling 1 that lower bound is ZERO: start at X.98 and the
# very first deadline check already reads X+1, so the loop breaks having taken a
# single sample. One sample is one run, one run is under any cap, nothing is
# elided, and the "further run(s) elided" line this step waits for never prints.
#
# Measured at 1 failure in 20 locally and hit on a CI runner, always this check.
# Nothing about it was a product fault, which is the part that makes it costly:
# a red build whose message reads "runs were dropped silently" sends the reader
# hunting for a reporting bug that does not exist.
#
# Ceiling 2 puts the floor at a full second -- about twenty samples at the 0.05
# interval set above -- against the three a cap of 2 needs to elide anything.
# That is a margin of roughly six times rather than the none it had.
reset; POLY_TRAJ_MAX_RUNS=2 poly_poll_until "the broker" never-matches 2 flapping \
  > "$WORK/f" 2>&1
runs=$(grep -c "^          t+" "$WORK/f")
[ "$runs" -le 2 ] \
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

step "8. poly_hold_field: a field that has stopped moving is reported as settled"
# The three cases below are issue #226's, and the order matters. The one that
# earns the check its keep is case 9: a floor of `-ge 2` on a switch count
# reported success on a tier that switched 80 times, so the assertion that
# matters is the one that FAILS on a value which will not sit still.
line_steady()      { printf 'primary 6 true 0'; return; }
line_field_moves() { printf 'primary %d true 0' "$(tick)"; return; }
line_other_moves() { printf 'primary 6 true %d' "$(tick)"; }

reset; poly_hold_field "the switch count" 2 1 4 line_steady > "$WORK/h" 2>&1
rc=$?
[ "$rc" -eq 0 ] && ok "a field that holds one value returns 0" || bad "expected rc=0, got $rc"
[[ "$POLY_HELD_VALUE" = "6" ]] \
  && ok "the settled value is reported for the caller's message ($POLY_HELD_VALUE)" \
  || bad "POLY_HELD_VALUE was '$POLY_HELD_VALUE', wanted 6"
[[ -s "$WORK/h" ]] \
  && bad "a settled field printed $(wc -l < "$WORK/h") line(s); it must be silent" \
  || ok "a settled field is silent"

step "9. poly_hold_field: a field that will not sit still is a failure, not a floor"
reset; start9=$(date +%s)
poly_hold_field "the switch count" 2 1 3 line_field_moves > "$WORK/i" 2>&1
rc=$?
elapsed9=$(( $(date +%s) - start9 ))
[ "$rc" -eq 1 ] \
  && ok "a field that changes on every sample returns 1" \
  || bad "flapping was reported as settled (rc=$rc) -- this is exactly issue #226"
[ "$POLY_HELD_CHANGES" -gt 1 ] \
  && ok "the report counts the changes it saw ($POLY_HELD_CHANGES)" \
  || bad "POLY_HELD_CHANGES was $POLY_HELD_CHANGES; a flapping field changed more than once"
grep -q "never held one value" "$WORK/i" \
  && ok "the failure says what was wrong rather than printing a bare count" \
  || bad "the failure did not name the property it was measuring"
[ "$elapsed9" -le 6 ] \
  && ok "the wait is bounded by its ceiling (${elapsed9}s against a ceiling of 3s)" \
  || bad "the wait ran ${elapsed9}s past a 3s ceiling"

step "10. poly_hold_field: only the named field decides"
# The whole LINE is recorded and a SINGLE FIELD is compared. Without the
# distinction this check would fail whenever any unrelated column moved -- and
# in the failover suite the destination restart count in field 4 moves for
# reasons that have nothing to do with switching.
reset; poly_hold_field "the switch count" 2 1 4 line_other_moves > "$WORK/j" 2>&1
rc=$?
[[ "$rc" -eq 0 ]] \
  && ok "movement in an unwatched field does not count as instability" \
  || bad "a field that never moved was reported unstable because its neighbours did (rc=$rc)"

step "11. poly_detectable_floor: what a green flake report did NOT measure"
# The numbers are checked against the closed form rather than against whatever
# the function currently prints, which is the difference between a test and a
# transcript. p = 1 - (1-c)^(1/N):
#   N=1,  c=95  ->  95.0   one run catches only a certainty
#   N=5,  c=95  ->  45.1   the workflow's default; this is the headline
#   N=40, c=95  ->   7.2   its cap, still above the ~7% rate #180 implies
[[ "$(poly_detectable_floor 1)" = "95.0" ]] \
  && ok "one run detects nothing below 95%" \
  || bad "floor(1) = $(poly_detectable_floor 1), want 95.0"
[[ "$(poly_detectable_floor 5)" = "45.1" ]] \
  && ok "the workflow default of 5 runs detects nothing below 45.1%" \
  || bad "floor(5) = $(poly_detectable_floor 5), want 45.1"
[ "$(poly_detectable_floor 40)" = "7.2" ] \
  && ok "even the cap of 40 runs cannot see the ~7% rate issue #180 implies" \
  || bad "floor(40) = $(poly_detectable_floor 40), want 7.2"

# Monotonicity, because a floor that did not fall with more runs would be
# describing something other than repetition.
f5=$(poly_detectable_floor 5); f20=$(poly_detectable_floor 20)
awk -v a="$f5" -v b="$f20" 'BEGIN{exit !(a > b)}' \
  && ok "more runs lower the floor ($f5% -> $f20%)" \
  || bad "the floor did not fall from 5 runs ($f5) to 20 ($f20)"

# A confidence that is not 95 has to move it, or the second argument is decoration.
awk -v a="$(poly_detectable_floor 5 99)" -v b="$f5" 'BEGIN{exit !(a > b)}' \
  && ok "demanding more confidence raises the floor" \
  || bad "the confidence argument changed nothing"

# Garbage in, nothing out. A floor printed from an unparseable run count would
# be a number in a report with no measurement behind it, which is the exact
# disease this whole function is for.
for junk in "" "abc" "-1" "0"; do
  if out=$(poly_detectable_floor "$junk" 2>&1) && [ -n "$out" ]; then
    bad "poly_detectable_floor '$junk' printed '$out' instead of refusing"
  else
    ok "poly_detectable_floor refuses '$junk'"
  fi
done

total=$((pass + fail))
EXPECTED_CHECKS=29
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
