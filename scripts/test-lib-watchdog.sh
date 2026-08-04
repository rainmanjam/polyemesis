#!/usr/bin/env bash
# Tests for lib-watchdog.sh -- the deadline that makes a hung suite explain
# itself instead of being cancelled by the job ceiling.
#
# WHY A TEST FOR TEST CODE
#
# The same argument as test-lib-observe.sh, and one extra that is specific to
# this file: a watchdog is a background process holding the suite's stdout. Get
# it wrong and it does not merely fail to report -- it becomes the hang. A CI
# step does not end until its stdout pipe closes, so a watchdog left running
# after a PASSING suite would sit there until the twenty-minute ceiling and be
# cancelled, which is precisely the symptom issue #38 is about. Case 5 exists
# for that, and it asserts on the real mechanism (the pipe closing) rather than
# on a proxy for it.
#
# The other load-bearing case is 6. The watchdog sends TERM rather than KILL so
# the suite's EXIT trap still reaps its FFmpeg children; a KILL would leave the
# runner holding relay ports and break the NEXT suite in the matrix, which would
# look like a flake in a suite that never had one.
#
# Usage:  ./scripts/test-lib-watchdog.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

# A stand-in for a real suite: sources the lib, arms the deadline, wires step()
# and the EXIT trap the way the acceptance suites do, then runs whatever body
# the test asks for.
#
# The deadline is one second and the tick is a fraction of one, because these
# tests assert on WHAT is reported, never on how long it took to report it.
# POLY_STEP_FILE is set to a known path rather than left to mktemp, so case 7
# can assert the breadcrumb was cleaned up. The acceptance suites leave it
# unset and get a temp file; the mechanism under test is the same either way.
make_suite() {
  local path="$1" body="$2"
  cat > "$path" <<EOF
#!/usr/bin/env bash
set -uo pipefail
. "$SCRIPTS/lib-watchdog.sh"
POLY_STEP_FILE="$path.step"
POLY_WATCHDOG_TICK=0.2
step()    { printf '\n%s\n' "\$1"; poly_step_record "\$1"; }
cleanup() { poly_watchdog_disarm; printf 'TRAP_RAN\n'; }
trap cleanup EXIT
cd "$WORK"
poly_watchdog_arm 1
$body
EOF
  chmod +x "$path"
}

# Run a fake suite and capture its output and exit code.
#
# The subshell with its stderr discarded is not decoration. When a foreground
# child is killed by a signal, the INVOKING shell prints "Terminated: 15" to its
# own stderr -- which lands in the middle of this suite's output and reads as a
# failure in a test that is passing. Only the outer redirect can suppress it,
# because the message does not come from the child.
run_suite() {
  local script="$1" out="$2"
  ( "$script" > "$out" 2>&1 ) 2>/dev/null
}

step "1. A suite that finishes in time is not touched, and the watchdog is silent"
make_suite "$WORK/quick.sh" '
step "1. something brief"
printf "SUITE_COMPLETED\n"
'
run_suite "$WORK/quick.sh" "$WORK/quick.out"
rc=$?
[ "$rc" -eq 0 ] && ok "a suite inside the deadline exits 0" \
                || bad "expected rc=0 from a suite that finished, got $rc"
grep -q "SUITE_COMPLETED" "$WORK/quick.out" \
  && ok "the suite ran to its end" \
  || bad "the suite did not reach its end"
grep -q "WATCHDOG" "$WORK/quick.out" \
  && bad "the watchdog reported on a suite that finished in time" \
  || ok "a suite that finishes in time gets no watchdog output"

step "2. A suite that hangs is killed, and the failure is not silent"
make_suite "$WORK/hang.sh" '
step "3. the step that wedges"
sleep 30
printf "SHOULD_NOT_REACH\n"
'
run_suite "$WORK/hang.sh" "$WORK/hang.out"
rc=$?
[ "$rc" -ne 0 ] && ok "a hung suite exits non-zero (rc=$rc)" \
                || bad "a hung suite exited 0 -- the hang would pass CI"
grep -q "SHOULD_NOT_REACH" "$WORK/hang.out" \
  && bad "the suite continued past the deadline; nothing was killed" \
  || ok "the suite was stopped at the deadline"
grep -q "WATCHDOG" "$WORK/hang.out" \
  && ok "the watchdog announced itself" \
  || bad "the suite was killed with no explanation -- the same as a job cancel"

step "3. The report names the step it was stuck in, not the first one"
grep -q "last step reached: 3. the step that wedges" "$WORK/hang.out" \
  && ok "the report names the last step entered" \
  || bad "the report did not name the step the suite was stuck in"
grep -q "entered .*s ago" "$WORK/hang.out" \
  && ok "the report says how long that step had been running" \
  || bad "the report gives no dwell time -- slow and wedged read the same"

# Two steps, so "last" is a claim with something to be wrong about. A report
# that printed the FIRST step would still pass case 3 above, because that suite
# had only one.
make_suite "$WORK/two.sh" '
step "1. the step that completes"
step "2. the step that wedges"
sleep 30
'
run_suite "$WORK/two.sh" "$WORK/two.out"
grep -q "last step reached: 2. the step that wedges" "$WORK/two.out" \
  && ok "with two steps it still names the later one" \
  || bad "the report named the wrong step: $(grep 'last step' "$WORK/two.out")"

step "4. A hang before the first step says so rather than reporting a blank"
make_suite "$WORK/early.sh" '
sleep 30
'
run_suite "$WORK/early.sh" "$WORK/early.out"
grep -q "hung before the first step" "$WORK/early.out" \
  && ok "a hang during setup is named as such" \
  || bad "a hang before any step produced an empty or misleading step line"

step "5. The watchdog does not outlive the suite, so the CI step can end"
# The real mechanism, not a proxy: a CI step ends when its stdout pipe closes,
# and a surviving background watchdog holds that pipe open. So run the suite
# through a pipe and watch for the pipe to close on its own.
("$WORK/quick.sh" | cat > "$WORK/pipe.out" 2>&1) &
pipe_pid=$!
for _ in $(seq 1 40); do
  kill -0 "$pipe_pid" 2>/dev/null || break
  sleep 0.25
done
if kill -0 "$pipe_pid" 2>/dev/null; then
  kill -9 "$pipe_pid" 2>/dev/null
  bad "stdout stayed open after the suite exited -- a live watchdog would hang the CI step"
else
  ok "stdout closes when the suite exits; no watchdog is left behind"
fi

step "6. The kill is a TERM, so the suite's teardown still runs"
# KILL would stop the suite just as dead and leave its FFmpeg children holding
# the relay ports the next suite in the matrix binds. That failure surfaces in
# a DIFFERENT suite, which is the most expensive kind to trace.
grep -q "TRAP_RAN" "$WORK/hang.out" \
  && ok "the EXIT trap ran after the watchdog fired" \
  || bad "the watchdog killed the suite before its teardown -- ports would leak to the next suite"

step "7. Disarming removes the breadcrumb it created"
# This check exists because the previous one does NOT cover disarming. A
# mutation making poly_watchdog_disarm a no-op left all twelve earlier checks
# green: the watchdog notices its suite has gone and exits on its own within a
# tick, so nothing about the pipe or the exit code moves. What does move is the
# leftover file, on every runner and every laptop, once per suite.
[ -f "$WORK/quick.sh.step" ] \
  && bad "the breadcrumb file survived a clean run -- disarming did not clean up" \
  || ok "the breadcrumb file is removed when the suite disarms"

total=$((pass + fail))
EXPECTED_CHECKS=13
printf "\n"
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -gt 0 ]; then
  printf "  \033[31mlib-watchdog: %d of %d checks FAILED\033[0m\n\n" "$fail" "$total"
  exit 1
fi
printf "  \033[32mlib-watchdog: %d checks passed\033[0m\n\n" "$total"
