#!/usr/bin/env bash
# Tests for lib-cleanup.sh's port reclaim.
#
# WHY THIS EXISTS
#
# The teardown it guards was wrong for a long time in a way nothing reported. It
# kills `ffmpeg.*$work`, which matches every child whose argv carries a path
# under the working directory -- and misses the INGEST, whose argv is only
# `-i rtmp://0.0.0.0:<port>/live ... udp://127.0.0.1:<relay>`. The leftover
# count used the same pattern, so the orphan was unkilled AND invisible.
#
# It took a 20-run measurement to find: acceptance-failover failed 10 of 20 back
# to back, every failure with "Address already in use" on the ingest port. A
# teardown that leaks is not a test-harness inconvenience -- it makes the harness
# measure itself instead of the suite, which is how a real regression would have
# been dismissed as "that suite is flaky".
#
# Usage:  ./scripts/test-lib-cleanup.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

if ! command -v lsof >/dev/null 2>&1; then
	echo "lsof is required (the helper uses it to find port holders)"
	exit 1
fi

# A port high enough that nothing else on a developer machine or a runner is
# plausibly using it.
PORT=19387
HOLDER=""

hold_port() {
	# python3 rather than `nc -l`, which does not bind on macOS -- it accepts the
	# flag, exits, and the test then "passes" against a port nothing ever held.
	# The same trap cost a real debugging detour earlier in this repo's history.
	python3 -c "
import socket, time
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', $PORT))
s.listen(1)
time.sleep(300)
" >/dev/null 2>&1 &
	HOLDER=$!
	# Detached so the shell does not announce "Killed: 9" when the code under
	# test reaps it -- which is the expected outcome, not an incident.
	disown "$HOLDER" 2>/dev/null || true
	for _ in $(seq 1 20); do
		[ -n "$(poly_port_holders "$PORT")" ] && return 0
		sleep 0.1
	done
	return 1
}

cleanup() { [ -n "$HOLDER" ] && kill -9 "$HOLDER" 2>/dev/null; }
trap cleanup EXIT

step "1. A free port is reported free, and costs nothing"
if [ -n "$(poly_port_holders "$PORT")" ]; then
	echo "port $PORT was already busy; choose another"
	exit 1
fi
start=$(date +%s)
poly_free_port "$PORT" >/dev/null 2>&1
took=$(( $(date +%s) - start ))
if [ "$took" -le 1 ]; then
	ok "a free port returns immediately (${took}s)"
else
	bad "a free port took ${took}s; the wait runs even when there is nothing to wait for"
fi

step "2. A held port is reclaimed"
if ! hold_port; then
	echo "could not hold $PORT with nc; skipping"
	exit 1
fi
reclaim=$(poly_free_port "$PORT" 2>&1)
if [ -z "$(poly_port_holders "$PORT")" ]; then
	ok "the port was reclaimed from a process that would not let go"
else
	bad "the port is still held; the next run would fail to bind"
fi
# Loudly, or the next reader has no way to tell a teardown that works from one
# that is being rescued every single time.
if printf '%s' "$reclaim" | grep -q "still held"; then
	ok "the forced reclaim says so rather than papering over it"
else
	bad "a port was taken back by force and nothing said so"
fi

step "3. A port released during the grace period is NOT killed"
# The distinction matters: a teardown that works releases the port a moment
# later, and killing immediately would hide that it works.
if ! hold_port; then
	echo "could not hold $PORT; skipping"
	exit 1
fi
( sleep 1; kill "$HOLDER" 2>/dev/null ) &
out=$(poly_free_port "$PORT" 2>&1)
if printf '%s' "$out" | grep -q "reclaiming it"; then
	bad "a port released inside the grace period was reported as reclaimed by force"
else
	ok "a port released during the wait is not reported as a leak"
fi
wait 2>/dev/null

step "4. Waiting for a listener returns as soon as one appears"
# The inverse of the reclaim, and the fix for the residual flake: a publisher
# started before the ingest binds is refused and exits.
( sleep 0.5; hold_port >/dev/null 2>&1 ) &
starter=$!
start=$(date +%s)
if poly_wait_port_ready "$PORT" 10 >/dev/null 2>&1; then
	took=$(( $(date +%s) - start ))
	if [ "$took" -le 5 ]; then
		ok "returned once something was listening (${took}s)"
	else
		bad "took ${took}s to notice a listener that appeared after 0.5s"
	fi
else
	bad "never noticed a listener that appeared during the wait"
fi
wait "$starter" 2>/dev/null
poly_free_port "$PORT" >/dev/null 2>&1

step "5. Waiting for a listener that never comes gives up loudly"
out=$(poly_wait_port_ready "$PORT" 1 2>&1)
if printf '%s' "$out" | grep -q "nothing is listening"; then
	ok "a port nobody binds is reported rather than waited on forever"
else
	bad "the wait gave up silently; the next failure would blame the source"
fi

step "6. An empty port argument is a no-op rather than an error"
if poly_free_port "" >/dev/null 2>&1; then
	ok "no port to free is not an error"
else
	bad "an empty port argument returned non-zero"
fi

total=$((pass + fail))
EXPECTED_CHECKS=7
printf "\n"
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
	printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
	exit 1
fi
if [ "$fail" -gt 0 ]; then
	printf "  \033[31mlib-cleanup: %d of %d checks FAILED\033[0m\n\n" "$fail" "$total"
	exit 1
fi
printf "  \033[32mlib-cleanup: %d checks passed\033[0m\n\n" "$total"
