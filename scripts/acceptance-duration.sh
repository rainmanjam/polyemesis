#!/usr/bin/env bash
#
# Run a broadcast long enough for the slow faults to show.
#
# #380's DURATION gap: "the longest suite is 75s; real broadcasts run for hours.
# Backoff crawl toward the 30s ceiling, memory growth, disk fill and reconnect
# churn are duration bugs and nothing would currently see them."
#
# All four are invisible to a 75-second suite by construction. A leak of 200 kB
# a minute is 12 MB an hour and a quarter of a megabyte in 75 seconds, which is
# allocator noise. A backoff that doubles per failure needs several failures to
# reach its ceiling. Churn is a COUNT that only becomes a rate once there is
# enough time to divide by.
#
# WHAT MAKES A DURATION SUITE WORTHLESS. Every trend it watches reads FLAT on an
# idle server: no memory growth, no restarts, no disk growth. So a long run
# against a broadcast that died in its first minute reports perfect health, at
# length and with graphs. The concurrency suite next door learned this from a
# per-destination cost that halved beautifully because the processes were dead.
#
# So delivery is asserted FIRST and everything else is conditional on it: the
# relay's byte counter must advance across EVERY interval, not just between the
# endpoints, because a stall in the middle that recovered is exactly the churn
# this is looking for and endpoints alone would average it away.
#
# TRENDS, NOT POINT VALUES. Peak RSS is a property of the machine; RSS climbing
# across an hour is a property of the code.
#
# Usage:  ./scripts/acceptance-duration.sh [workdir]
#   DURATION_MINUTES=10   how long to sample for
#   DURATION_DESTS=2      how many destinations to run
set -u

WORK="${1:-/tmp/polyemesis-acceptance-duration}"
PORT=8099
MINUTES="${DURATION_MINUTES:-10}"
DESTS="${DURATION_DESTS:-2}"

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
  pkill -f "acceptance-duration-source" 2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK"
}
trap 'poly_teardown_trap $? cleanup' EXIT

poly_require_exec "$BIN"
poly_require_cmd go "needed to run the acceptance driver via 'go run'"
poly_require_cmd ffmpeg
poly_require_cmd ps "the memory trend is read out of the process table"
poly_require_cmd du "the disk trend is read off the data directory"

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

step "2. Broadcast for $MINUTES minute(s) with $DESTS destination(s)"
FACTS="$WORK/facts.env"
# RUN FROM $ROOT, IN A SUBSHELL: `go run` resolves module imports against the
# CURRENT directory's go.mod, and this suite has cd'd into a workdir under /tmp
# that is inside no module. driverlib's package comment records the same trap.
( cd "$ROOT" && go run "$SCRIPTS/acceptance_duration_driver.go" \
    "$PORT" "$RELAY" "$FACTS" "$MINUTES" "$DESTS" "$WORK/data" 2>&1 ) | sed 's/^/  /'

[[ -s "$FACTS" ]] || { bad "driver wrote no facts"; step "Summary"; printf "  %d passed, %d failed\n\n" "$pass" "$fail"; exit 1; }
# shellcheck disable=SC1090
source "$FACTS"
if [[ -n "${DRIVER_FAILED:-}" ]]; then bad "driver aborted: $DRIVER_FAILED"; fi

# ------------------------------------------------------------- 3. delivery
#
# BEFORE EVERY OTHER CHECK, because every other check passes trivially on a
# server that was doing nothing. This is the one that makes the rest mean
# something.
step "3. It was actually broadcasting the whole time"
RX_DELTA=$(( ${DUR_RX_END:-0} - ${DUR_RX_START:-0} ))
if [[ "$RX_DELTA" -gt 1000000 ]]; then
  ok "the relay took $(( RX_DELTA / 1000000 )) MB across the window"
else
  bad "the relay took only ${RX_DELTA} bytes across the window"
  note "Every trend below reads flat on an idle server, so they are not"
  note "evidence of anything while this is failing. Check the source"
  note "published at all, and that /status still reports rxBytes where this"
  note "expects it -- a shape change here looks exactly like a dead broadcast."
fi
if [[ "${DUR_STALLS:-1}" == "0" ]]; then
  ok "no sampling interval saw the byte counter stall"
else
  bad "${DUR_STALLS} interval(s) saw no bytes arrive"
  note "A stall that recovered still happened. Endpoints alone would have"
  note "averaged it away, which is why every interval is checked."
fi

# --------------------------------------------------------------- 4. churn
step "4. Nothing reconnected"
if [[ "${DUR_RESTARTS_END:-1}" == "${DUR_RESTARTS_START:-0}" ]]; then
  ok "no ingest or destination restarted (${DUR_RESTARTS_END:-?} total, unchanged)"
else
  bad "restarts went ${DUR_RESTARTS_START:-?} -> ${DUR_RESTARTS_END:-?}"
  note "Reconnect churn is the fault this measures: a healthy broadcast does"
  note "not restart anything, and a count that climbs with wall time is the"
  note "backoff crawl #380 names."
fi

# -------------------------------------------------------------- 5. memory
#
# A CEILING ON THE RATE, not on the value. Go's heap and the GC make RSS noisy
# enough that a tight bound would fail on scheduling; what a leak looks like is
# a rate that does not fall off.
step "5. Memory is not climbing"
# GATED ON THE POST-WARM-UP RATE, not the overall one. A server's first minute
# allocates caches, opens files and starts children, and on a short run that
# startup dominates the overall figure entirely -- a two-minute run read
# 1535 kB/min overall against 912 kB across its whole last third. Gating on the
# overall number would need a ceiling loose enough to catch nothing, or would
# fail on run LENGTH rather than on a leak.
RSS_TAIL="${DUR_RSS_TAIL_PER_MIN_KB:-0}"
CEIL=1024
# A MINIMUM RUN LENGTH, because this gate is not meaningful below one and
# saying so is better than firing. Measured on an idle-but-broadcasting server:
# the post-warm-up slope reads 1255 kB/min over a 3-minute run and 239 kB/min
# over a 10-minute one, on the same machine with the same workload. Nothing
# leaked; the heap was still climbing toward steady state, and a shorter window
# is mostly measuring that climb no matter how the warm-up is excluded.
#
# So below MIN_GATE_MIN this REPORTS and does not gate. A short run is still
# worth doing -- it exercises delivery, churn and disk -- but a memory verdict
# from one would be a false alarm about arithmetic rather than a finding about
# the code.
MIN_GATE_MIN=5
if awk -v m="${DUR_MINUTES:-0}" -v n="$MIN_GATE_MIN" 'BEGIN{exit !(m < n)}'; then
  ok "server RSS moved ${RSS_TAIL} kB/min after warm-up (not gated: needs >= ${MIN_GATE_MIN} min)"
  note "A run this short is dominated by the heap reaching steady state. The"
  note "same server reads 1255 kB/min over 3 minutes and 239 over 10, with"
  note "nothing leaking. Run with DURATION_MINUTES=${MIN_GATE_MIN} or more for a verdict."
elif awk -v r="$RSS_TAIL" -v c="$CEIL" 'BEGIN{exit !(r <= c)}'; then
  ok "server RSS moved ${RSS_TAIL} kB/min after warm-up (ceiling ${CEIL})"
else
  bad "server RSS grew ${RSS_TAIL} kB/min after warm-up, over the ${CEIL} kB/min ceiling"
  note "At that rate an eight-hour broadcast would add about $(awk -v r="$RSS_TAIL" 'BEGIN{printf "%.0f", r*480/1024}') MB."
  note "Overall rate including warm-up was ${DUR_RSS_PER_MIN_KB:-?} kB/min."
fi
# Reported rather than gated. Warm-up allocates and a leak does not stop, so
# the two thirds tell them apart -- but GC timing makes this noisy enough that
# failing on it would cost more in false alarms than it catches.
note "first third ${DUR_RSS_EARLY_KB:-?} kB, last third ${DUR_RSS_LATE_KB:-?} kB"
note "(warm-up shows in the first and not the last; a leak shows in both)"

# ---------------------------------------------------------------- 6. disk
step "6. Disk growth is the recordings and nothing else"
# The destinations are writing files, so growth is EXPECTED and roughly the
# media bitrate. What this refuses is growth far beyond it -- logs, temp files,
# or a recording nothing rotates. The bound is generous: the source is 3000k
# video plus 128k audio, so about 23 MB a minute per destination.
BUDGET=$(awk -v m="${DUR_MINUTES:-1}" -v d="${DUR_DESTS_WANTED:-1}" 'BEGIN{printf "%d", m*d*23*1024*2}')
GROWTH="${DUR_DATA_GROWTH_KB:-0}"
if [[ "$GROWTH" -le "$BUDGET" ]]; then
  ok "data directory grew ${GROWTH} kB, within the ${BUDGET} kB media budget"
else
  bad "data directory grew ${GROWTH} kB, past the ${BUDGET} kB media budget"
  note "That is more than twice the media the destinations should have"
  note "written, so something else on disk is growing with wall time."
fi

# ------------------------------------------------------------ 7. still up
step "7. Everything was still running at the end"
if [[ "${DUR_DESTS_UP_END:-0}" == "${DUR_DESTS_WANTED:-1}" ]]; then
  ok "all ${DUR_DESTS_WANTED:-?} destination(s) were running at the end"
else
  bad "${DUR_DESTS_UP_END:-0} of ${DUR_DESTS_WANTED:-?} destination(s) running at the end"
fi

step "Summary"
printf "  %d passed, %d failed  (%s minutes, %s samples)\n\n" \
  "$pass" "$fail" "${DUR_MINUTES:-?}" "${DUR_SAMPLES:-?}"
[[ "$fail" -eq 0 ]] || exit 1
