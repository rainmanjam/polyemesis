#!/usr/bin/env bash
#
# Break a healthy broadcast on purpose, and see whether it comes back.
#
# #380's MID-STREAM FAILURE gap: "acceptance-failover covers a switch; nothing
# covers a platform refusing at minute 30 of a healthy broadcast, a network
# drop, or the encoder dying mid-stream."
#
# The distinction is the whole point. Failover is a DECISION the engine makes
# when a source is absent at the moment it looks. This is a fault that ARRIVES
# into something already working -- everything probed, every process up, bytes
# moving -- which is the state a real broadcast is in for all but its first few
# seconds, and the state nothing here has ever tested.
#
# WHAT MAKES A RECOVERY SUITE WORTHLESS, and it is the exact mirror of the trap
# the concurrency suite next door records. That one learned that a
# per-destination cost halves beautifully when the processes are dead. Here the
# lie runs the other way: a recovery suite whose fault DID NOT LAND reports a
# perfect recovery, at length, with every check green. Nothing was broken, so of
# course nothing stayed broken.
#
# So every injection is a PAIR -- the fault, and a positive control proving it
# reached the product. A run whose control does not fire reports THE FAULT DID
# NOT LAND and fails. It never reports recovery it did not observe.
#
# AND RECOVERY IS ASSERTED ON DELIVERY, not on state. "running" is what a
# destination subscribed to nothing also says; bytes moving is the claim a green
# card cannot fake.
#
# Usage:  ./scripts/acceptance-faults.sh [workdir]
set -u

WORK="${1:-/tmp/polyemesis-acceptance-faults}"
PORT=8101

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"
. "$SCRIPTS/lib-watchdog.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"
. "$SCRIPTS/lib-preflight.sh"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
note() { printf "        %s\n" "$1"; }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-faults-source" 2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK"
}
trap 'poly_teardown_trap $? cleanup' EXIT

poly_require_exec "$BIN"
poly_require_cmd go "needed to run the acceptance driver via 'go run'"
poly_require_cmd ffmpeg

rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK" || exit 1
poly_watchdog_arm

step "1. Start the binary"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do
  sleep 0.3
  if grep -q "web ui" server.log 2>/dev/null; then break; fi
done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || bad "server did not start"

SRVPID=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
RELAY=$(lsof -nP -iUDP -a -p "$SRVPID" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}')

step "2. Establish a healthy broadcast, then break it"
FACTS="$WORK/facts.env"
# RUN FROM $ROOT, IN A SUBSHELL: `go run` resolves module imports against the
# CURRENT directory's go.mod, and this suite has cd'd into a workdir under /tmp
# that is inside no module. driverlib's package comment records the same trap.
( cd "$ROOT" && go run "$SCRIPTS/acceptance_faultinject_driver.go" \
    "$PORT" "$RELAY" "$FACTS" 2>&1 ) | sed 's/^/  /'

[[ -s "$FACTS" ]] || { bad "driver wrote no facts"; step "Summary"; printf "  %d passed, %d failed\n\n" "$pass" "$fail"; exit 1; }
# shellcheck disable=SC1090
source "$FACTS"
if [[ -n "${DRIVER_FAILED:-}" ]]; then bad "driver aborted: $DRIVER_FAILED"; fi

# ------------------------------------------------- 3. the faults landed
#
# BEFORE ANY RECOVERY CHECK, because a recovery claim about a fault that never
# arrived is worse than no claim: it is a green run asserting a property that
# was never exercised.
step "3. Each fault actually reached the product"
if [[ "${FAULT_ENDPOINT_LANDED:-false}" == "true" ]]; then
  ok "closing the destination's endpoint moved a restart counter"
else
  bad "closing the endpoint changed nothing the product reports"
  note "The destination had not connected to the sink, or the supervisor did not"
  note "notice within 90s. Either way the recovery result below is vacuous --"
  note "nothing was broken, so surviving proves nothing."
fi
if [[ "${FAULT_INGEST_LANDED:-false}" == "true" ]]; then
  ok "killing the publisher stopped the relay's byte counter"
else
  bad "the publisher was killed and the relay kept advancing"
  note "Something else is still publishing to the relay port. The ingest-recovery"
  note "result below is measuring a stream that never stopped."
fi

# ------------------------------------------------- 4. and it came back
step "4. The broadcast came back on its own"
if [[ "${FAULT_ENDPOINT_RECOVERED:-false}" == "true" ]]; then
  ok "the destination reconnected once its endpoint returned"
else
  bad "the destination never came back after its endpoint returned"
  note "Nothing crashed; the broadcast simply stopped. That is the failure this"
  note "suite exists for -- a supervisor that gives up, or backs off past the"
  note "window, leaves an operator with a green console and no stream."
fi
if [[ "${FAULT_INGEST_RECOVERED:-false}" == "true" ]]; then
  ok "delivery resumed when the publisher came back"
else
  bad "the relay never advanced again after the publisher returned"
  note "A dropped ingest that needs a restart to recover is a broadcast lost to"
  note "any network blip, which is the ordinary condition of a real one."
fi

# ------------------------------------------------- 5. still delivering
#
# The end state, asserted on BYTES rather than on process state, for the reason
# the driver's own comment gives: a destination subscribed to nothing reports
# itself running for ever.
step "5. It is still delivering at the end"
if [[ "${FAULT_DELIVERING_AT_END:-false}" == "true" ]]; then
  ok "the relay was still advancing when the run finished"
else
  bad "the relay was not advancing at the end of the run"
fi
note "restarts ${FAULT_BASE_RESTARTS:-?} -> ${FAULT_FINAL_RESTARTS:-?}, ${FAULT_FINAL_UP:-?} destination(s) up"

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[[ "$fail" -eq 0 ]] || exit 1
