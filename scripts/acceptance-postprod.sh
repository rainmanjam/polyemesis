#!/usr/bin/env bash
# Post-production acceptance test.
#
# The governing principle of the post-production tier is that nothing may ever
# degrade the live stream: every heavy task is a queued job governed by a
# resource policy that yields to the stream by default. That is a claim about
# runtime behaviour, and this file MEASURES it rather than trusting the code:
#
#   * a queued job does NOT run while the ingest is delivering
#   * the same job DOES start once the stream stops
#   * when the stream comes back, the running job is allowed to finish and no
#     NEW job starts — which is exactly what the governor documents for a kind
#     with no Suspender (SuspendFinish, "finish-then-yield"), and nothing is
#     ever cancelled to enforce a gate
#   * a scheduled kind does not run outside its window
#   * jobs left running by a crash are recovered on the next start rather than
#     stranded in "running" forever
#
# whisper.cpp is NOT required. The heavy job is proxy generation, which needs
# only the FFmpeg the product already requires.
#
# Usage:  ./scripts/acceptance-postprod.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-postprod}"
PORT=8097
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

# Deliberately narrow: pkill on the bare binary name would take out another
# agent's or another operator's server on a different port.
cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup "$PORT" "${WORK:-}"
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"

start_server() {
  "$BIN" -addr ":$PORT" -data ./data -log warn >> server.log 2>&1 &
  for _ in $(seq 1 60); do
    sleep 0.3
    if curl -sf "http://127.0.0.1:$PORT/api/v1/health" > /dev/null 2>&1; then return 0; fi
  done
  return 1
}

relay_port() {
  local pid
  pid=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
  lsof -nP -iUDP -a -p "$pid" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}'
}

# ---------------------------------------------------------------- 1. server
step "1. Start the binary"
start_server && ok "server started" || { bad "server did not start"; exit 1; }

RELAY=$(relay_port)
[ -n "$RELAY" ] && ok "relay hub bound (udp/$RELAY)" || bad "no relay port"

# whisper is optional and this machine may well not have it. The server must be
# up either way, and the jobs page must report the fact rather than erroring.
if command -v whisper-cli > /dev/null 2>&1 || command -v whisper > /dev/null 2>&1; then
  printf "  note: whisper is installed on this machine\n"
else
  ok "the server started with whisper ABSENT (every external tool is optional)"
fi

# --------------------------------------------------------- 2. the governor
step "2. Measure the governor against a real stream and a real queue"
go run "$SCRIPTS/acceptance_postprod_driver.go" "$PORT" "$RELAY" 2>&1 | sed 's/^/  /'
DRIVER=${PIPESTATUS[0]}
[ "$DRIVER" -eq 0 ] && ok "governor measurements passed" || bad "governor measurements failed"

# ------------------------------------------------- 3. resume after restart
#
# A job interrupted by a crash must come back. The queue writes a job's outcome
# durably before it releases its concurrency slot, so a process killed mid-job
# leaves a row saying "running" and nothing that will ever finish it — recovery
# at startup is the only thing standing between that and a job stranded forever.
step "3. A job interrupted by a crash is recovered, not stranded"

JOB=$(go run "$SCRIPTS/acceptance_postprod_restart.go" "$PORT" arm 2>/dev/null | tail -1)
# Numeric, not merely non-empty. "Non-empty" is satisfied by an error message,
# which is exactly how a failure here once announced itself as a passing check.
case "$JOB" in ''|*[!0-9]*) JOB="" ;; esac
if [ -z "$JOB" ]; then
  bad "could not get a job running, so a crash could not be staged"
else
  ok "job $JOB is running"

  pkill -9 -f "polyemesis -addr :$PORT" 2>/dev/null
  sleep 2
  ok "server killed mid-job (SIGKILL, no clean shutdown, no chance to tidy up)"

  if start_server; then
    ok "server restarted after the kill"
    # The queue calls Recover() before it claims anything, so give the run loop
    # a moment to have done it.
    sleep 4
    while IFS= read -r line; do
      case "$line" in
        PASS*) ok "${line#PASS }" ;;
        FAIL*) bad "${line#FAIL }" ;;
        *)     printf "  %s\n" "$line" ;;
      esac
    done < <(go run "$SCRIPTS/acceptance_postprod_restart.go" "$PORT" check "$JOB" 2>&1)
  else
    bad "server did not restart after the kill"
  fi
fi

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"

# A count check, not just a failure check.
#
# This suite reported "12 passed, 0 failed / PASSED" and, on a loaded machine,
# "7 passed, 0 failed / PASSED" -- because a driver that exits non-zero prints
# FATAL and stops, and the sections after it simply never run. Zero failures
# out of seven assertions is not a pass, it is five assertions that were never
# asked, and nothing on screen distinguished the two.
#
# Shingo's fixed-value method: confirm the COUNT. A run that performs fewer
# checks than the suite contains has not passed, whatever its failure tally
# says.
#
# The usual cause is real and environmental rather than a code fault: the job
# governor defers work while the machine is busy, so on a loaded box the
# crash-recovery section cannot stage its job. That is the governor behaving as
# designed. What was wrong was calling it a pass.
EXPECTED_CHECKS=12
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran; the rest never executed.\n" \
    "$total" "$EXPECTED_CHECKS"
  printf "  A section exited early -- look for FATAL above. On a loaded machine the\n"
  printf "  job governor defers work, which starves the crash-recovery section.\n\n"
  exit 1
fi

if [ "$fail" -eq 0 ]; then
  printf "  \033[32mPOST-PRODUCTION ACCEPTANCE PASSED\033[0m\n\n"
  exit 0
fi
printf "  \033[31mPOST-PRODUCTION ACCEPTANCE FAILED\033[0m\n\n"
exit 1
