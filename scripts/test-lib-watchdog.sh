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
  # THE VERDICT BEFORE THE CLEANUP, which is the ordering #179 cost 28 minutes
  # and an unrecoverable log to establish: an answer that has been computed must
  # be emitted before anything that can hang or be misread runs.
  bad "stdout stayed open after the suite exited -- a live watchdog would hang the CI step"
  kill -9 "$pipe_pid" 2>/dev/null
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

step "8. The process list matches on argv, not on the truncated COMM column"
# This is a regression guard for a bug that shipped past its own review. The
# first version matched the COMM column. Linux prints it in full, so CI was
# green; macOS truncates it to sixteen characters, so a laptop run reported
# "nothing of ours was still running" with four FFmpegs live on screen.
#
# The fixture is real `ps` output from both platforms, pasted rather than
# generated, because the whole bug was a disagreement about the format.
. "$SCRIPTS/lib-watchdog.sh"
cat > "$WORK/ps.txt" <<'PSEOF'
  PID     ELAPSED COMM             ARGS
88943 00:12 /opt/homebrew/bi /opt/homebrew/bin/ffmpeg -hide_banner -i udp://127.0.0.1:21003
88944 00:12 polyemesis       /Users/x/Documents/polyemesis/polyemesis -addr :8092 -data ./data
88945 00:12 bash             /bin/bash /Users/x/Documents/polyemesis/scripts/acceptance-synth.sh
  742 01:03 sshd             /usr/sbin/sshd -D
PSEOF
matched="$(poly__watchdog_match_procs < "$WORK/ps.txt")"
printf '%s' "$matched" | grep -q "ffmpeg -hide_banner" \
  && ok "an FFmpeg whose COMM was truncated by macOS is still found" \
  || bad "the macOS-truncated COMM line was missed -- the report would say nothing is running"
printf '%s' "$matched" | grep -q "polyemesis -addr" \
  && ok "the engine is found" \
  || bad "the engine was missed"
printf '%s' "$matched" | grep -q "acceptance-synth.sh" \
  && bad "the suite's own shell was reported as a live process (its path contains 'polyemesis')" \
  || ok "the suite's own shell is not mistaken for one of its children"
printf '%s' "$matched" | grep -q "sshd" \
  && bad "an unrelated process was reported" \
  || ok "unrelated processes are left out"

step "9. A suite whose run length is a parameter gets a deadline that outlives it"
# acceptance-duration.sh broadcasts for DURATION_MINUTES and armed the default
# deadline, 900s. So DURATION_MINUTES=30 -- the run the staging-readiness
# review asked for, and the one its own header says the slow faults need --
# was killed at minute fifteen with a WATCHDOG report, every time, having
# measured nothing. The deadline is a hang detector; a run that is long on
# purpose is not a hang. make_suite arms one second first, so this also proves
# the longer deadline REPLACES the default rather than running beside it.
make_suite "$WORK/long.sh" '
POLY_WATCHDOG_SECS=1 POLY_WATCHDOG_RUN_HEADROOM=1
poly_watchdog_arm_for 2
step "1. a run that is long on purpose"
sleep 2
printf "SUITE_COMPLETED\n"
'
run_suite "$WORK/long.sh" "$WORK/long.out"
rc=$?
if [ "$rc" -eq 0 ] && grep -q SUITE_COMPLETED "$WORK/long.out" && ! grep -q "WATCHDOG" "$WORK/long.out"; then
  ok "a run inside its own length plus headroom is not killed by the shorter default"
else
  bad "a run shorter than its declared length was killed (rc=$rc):"
  sed 's/^/        /' "$WORK/long.out"
fi

# Still a deadline: past its length plus headroom, it fires as before.
make_suite "$WORK/longhang.sh" '
POLY_WATCHDOG_SECS=1 POLY_WATCHDOG_RUN_HEADROOM=0
poly_watchdog_arm_for 1
step "1. a long run that then wedges"
sleep 10
printf "SUITE_COMPLETED\n"
'
run_suite "$WORK/longhang.sh" "$WORK/longhang.out"
if grep -q "=== WATCHDOG" "$WORK/longhang.out" && ! grep -q SUITE_COMPLETED "$WORK/longhang.out"; then
  ok "a run past its length plus headroom is still killed and reported"
else
  bad "poly_watchdog_arm_for never fired on a run past its length"
fi

# And the suite that needs it uses it. Read rather than run: running it is a
# thirty-minute broadcast.
if grep -qE '^poly_watchdog_arm_for .*MINUTES' "$SCRIPTS/acceptance-duration.sh" \
   && ! grep -qE '^poly_watchdog_arm *$' "$SCRIPTS/acceptance-duration.sh"; then
  ok "acceptance-duration.sh arms its deadline from DURATION_MINUTES"
else
  bad "acceptance-duration.sh does not arm with poly_watchdog_arm_for and its MINUTES; any DURATION_MINUTES over ~14 is killed by the 900s default"
fi

step "10. A suite the watchdog killed never prints POLY-VERDICT: PASS"
# Found by the same thirty-minute run. The watchdog fired at 901s, the suite's
# teardown trap ran -- and its last line was "POLY-VERDICT: PASS". The trap is
# handed `$?`, and after a TERM that is the status of whatever last finished,
# usually 0. lib-preflight.sh's whole contract is that a KILL takes the pass
# token with it; here the kill manufactured one. This case uses the real
# teardown trap the suites use, not make_suite's stand-in.
cat > "$WORK/verdict.sh" <<EOF
#!/usr/bin/env bash
set -uo pipefail
. "$SCRIPTS/lib-cleanup.sh"
. "$SCRIPTS/lib-watchdog.sh"
POLY_STEP_FILE="$WORK/verdict.sh.step"
POLY_WATCHDOG_TICK=0.2
cleanup() { return "\${1:-0}"; }   # as poly_cleanup_exit does: the status it is handed
trap 'poly_teardown_trap \$? cleanup' EXIT
cd "$WORK"
poly_watchdog_arm 1
true
sleep 10
EOF
chmod +x "$WORK/verdict.sh"
run_suite "$WORK/verdict.sh" "$WORK/verdict.out"
rc=$?
if grep -q "POLY-VERDICT: FAIL" "$WORK/verdict.out" && ! grep -q "POLY-VERDICT: PASS" "$WORK/verdict.out"; then
  ok "a watchdog kill ends in POLY-VERDICT: FAIL (rc=$rc)"
else
  bad "a watchdog-killed suite printed $(grep -o 'POLY-VERDICT: [A-Z]*' "$WORK/verdict.out" || echo 'no verdict') (rc=$rc)"
fi
[ -e "$WORK/verdict.sh.step.fired" ] \
  && bad "the watchdog's fired marker survived the teardown" \
  || ok "the fired marker is cleaned up with the breadcrumb"

total=$((pass + fail))
EXPECTED_CHECKS=22
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
