#!/usr/bin/env bash
#
# What does a destination cost, and do N of them cost N times it?
#
# #380's concurrency gap. README.md and docs/COMPARISON.md both publish "roughly
# 4% of one core per destination" and nothing tests it -- a reader sizes a box
# with that number.
#
# THE FALSE RESULT THIS SUITE REFUSES. Measuring this by hand, against raw
# ffmpeg rather than through the product, produced:
#
#     1 destination:  3.80% of a core
#     2 destinations: 2.23% each
#     4 destinations: 1.11% each
#     8 destinations: 0.55% each
#
# Per-destination cost halving with every doubling, which is exactly the result
# anyone measuring this WANTS. It was an artefact: every run had one surviving
# process. Several ffmpeg readers on one UDP unicast socket compete for packets
# and all but one die, so the "total" was one survivor's CPU divided by N.
#
# It was caught only because the harness happened to count survivors. So this
# suite asserts LIVENESS BEFORE COST, everywhere, and the driver refuses to
# report a number without it. The product does not have the competing-reader
# problem -- internal/relay.Hub gives each destination its own subscription port
# -- which is why this measures through the real server.
#
# WHAT IT ASSERTS IS THE SHAPE, NOT THE PERCENTAGE. "4% of a core" is a property
# of a machine: the same destination measures 8.8% on Apple silicon against a
# published 4% on a six-core VPS, and neither is wrong. A suite pinning a number
# would fail on hardware rather than on regressions. The absolute figure is
# PRINTED every run, because it is what the README claims and somebody should be
# able to read it off a CI log.
#
# Usage:  ./scripts/acceptance-concurrency.sh [workdir]
set -u

WORK="${1:-/tmp/polyemesis-acceptance-concurrency}"
PORT=8098
# How many destinations the N case runs. Six rather than sixteen because the
# smallest CI runner has two cores and this is a linearity check, not a load
# test: sixteen stream-copy destinations on two cores measures the runner's
# scheduler, not the product.
DESTS="${CONCURRENCY_DESTS:-6}"

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
  pkill -f "acceptance-concurrency-source" 2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK"
}
trap 'poly_teardown_trap $? cleanup' EXIT

poly_require_exec "$BIN"
poly_require_cmd go "needed to run the acceptance driver via 'go run'"
poly_require_cmd ffmpeg
poly_require_cmd ps "the measurement reads cumulative CPU out of the process table"

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

step "2. Measure one destination, then $DESTS (via the API the UI uses)"
FACTS="$WORK/facts.env"
# RUN FROM $ROOT, IN A SUBSHELL. `go run` resolves a module import against the
# CURRENT directory's go.mod rather than the source file's location, and this
# suite has already cd'd into $WORK, which is under /tmp and inside no module.
# The same trap is recorded in driverlib's package comment.
( cd "$ROOT" && go run "$SCRIPTS/acceptance_concurrency_driver.go" \
    "$PORT" "$RELAY" "$FACTS" "$DESTS" 2>&1 ) | sed 's/^/  /'

[[ -s "$FACTS" ]] || { bad "driver wrote no facts"; step "Summary"; printf "  %d passed, %d failed\n\n" "$pass" "$fail"; exit 1; }
# shellcheck disable=SC1090
source "$FACTS"

if [[ -n "${DRIVER_FAILED:-}" ]]; then bad "driver aborted: $DRIVER_FAILED"; fi

# ------------------------------------------------------------------ 3. alive
#
# BEFORE ANY COST CHECK. This is the whole lesson of the false result above: a
# per-destination figure computed over processes that are no longer running is
# not a small error, it is a number pointing the wrong way.
step "3. Every destination that was asked for is still running"
if [[ "${CONC_ALIVE_1:-0}" == "1" ]]; then
  ok "the single destination survived its measurement window"
else
  bad "the single destination did not survive: alive=${CONC_ALIVE_1:-0}"
fi
if [[ "${CONC_ALIVE_N:-0}" == "$DESTS" ]]; then
  ok "all $DESTS destinations survived their measurement window"
else
  bad "asked for $DESTS destinations, ${CONC_ALIVE_N:-0} were alive at the end"
  note "This is the failure the suite exists to catch. Every per-destination"
  note "number below is meaningless while it holds -- dividing one survivor's"
  note "CPU by $DESTS is what produced a perfect halving and looked like news."
fi

# ------------------------------------------------------------------ 4. work
step "4. Every destination did measurable work"
if [[ "${CONC_ZERO_CPU_N:-1}" == "0" ]]; then
  ok "no destination burned zero CPU across the window"
else
  bad "${CONC_ZERO_CPU_N} destination(s) burned no measurable CPU"
  note "A process that is alive and costs nothing is not delivering. Alive is"
  note "necessary and not sufficient, which is why this check is separate."
fi

# ------------------------------------------------------------- 5. linearity
#
# THE PROPERTY, rather than a percentage. What must hold on every machine is
# that the Nth destination costs about what the first did. The bounds are wide
# on purpose: a two-core runner with six destinations contends, and contention
# is a fact about the runner.
step "5. The Nth destination costs about what the first did"
RATIO="${CONC_RATIO:-0}"
LOW=0.4
HIGH=2.5
if awk -v r="$RATIO" -v lo="$LOW" -v hi="$HIGH" 'BEGIN{exit !(r>=lo && r<=hi)}'; then
  ok "per-destination cost at $DESTS is ${RATIO}x the cost at 1 (within ${LOW}-${HIGH})"
else
  bad "per-destination cost at $DESTS is ${RATIO}x the cost at 1, outside ${LOW}-${HIGH}"
  if awk -v r="$RATIO" -v lo="$LOW" 'BEGIN{exit !(r<lo)}'; then
    note "BELOW the band, which is the direction the false result went. Check"
    note "the liveness numbers above before reading this as good news: work"
    note "that appears to vanish as load grows usually has not been done."
  else
    note "ABOVE the band: destinations are costing more together than apart,"
    note "which is contention. On a small runner that may be the runner."
  fi
fi

# ------------------------------------------------------------- 6. the figure
#
# Printed rather than asserted. It is hardware-dependent and a gate on it would
# fail on machines rather than on regressions -- but it is the number the README
# publishes, and nobody should have to build a harness to read it.
step "6. What it actually cost, on this machine"
printf "  one destination        %s%% of a core\n" "${CONC_PER_1_PCT:-?}"
printf "  each of %-2s            %s%% of a core\n" "$DESTS" "${CONC_PER_N_PCT:-?}"
printf "  total for %-2s          %s cores\n" "$DESTS" "${CONC_CORES_N:-?}"
note "README.md and docs/COMPARISON.md publish \"roughly 4% of one core per"
note "destination\", measured on a six-core VPS. This machine is not that"
note "machine; a difference here is not by itself a defect in either."

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[[ "$fail" -eq 0 ]] || exit 1
