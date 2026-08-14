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
releaser=$!
out=$(poly_free_port "$PORT" 2>&1)
if printf '%s' "$out" | grep -q "reclaiming it"; then
	bad "a port released inside the grace period was reported as reclaimed by force"
else
	ok "a port released during the wait is not reported as a leak"
fi
# `wait "$releaser"` and not a bare `wait`: a bare wait blocks on every job this
# shell has, including any future one somebody adds above, and this file is the
# one that argues unbounded waits in teardown are the defect. The releaser is a
# `sleep 1` and a kill, so waiting on it by pid is bounded by construction.
wait "$releaser" 2>/dev/null

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

step "7. poly_stop_server's kill -9 is re-observed, not assumed"
# The last step of that helper used to be `pkill -9` followed by a return, which
# asserted nothing: a signal is a request, and every caller of poly_stop_server
# proceeds as though the server were gone -- rebinding its port, reading the
# recordings it had open. That is the #179/#180 family in shell form.
#
# The fake ignores SIGTERM (so the graceful loop is exhausted and the escalation
# is actually reached) and carries the command line the helper matches on. exec
# keeps it to one process, and an ignored disposition survives exec.
#
# WHAT THIS CAN AND CANNOT SHOW. It shows the success path: when the helper
# returns, the server is already gone, checked at that instant with no sleep in
# between. It cannot stage a process that survives SIGKILL, so the loud-failure
# path is verified by mutation instead -- replace the `pkill -9` line with `:`
# and this case reports the FAIL banner within ~3s. Do that if you change the
# helper.
deaf_server() {
	bash -c 'trap "" TERM; exec -a "polyemesis -addr :'"$PORT"'" sleep 300' &
	DEAF=$!
	disown "$DEAF" 2>/dev/null || true
	for _ in $(seq 1 20); do
		pgrep -f "polyemesis -addr :$PORT" >/dev/null 2>&1 && return 0
		sleep 0.1
	done
	return 1
}

if ! deaf_server; then
	bad "could not stage a server that ignores SIGTERM"
else
	if poly_stop_server "$PORT" >/dev/null 2>&1; then
		ok "poly_stop_server reports success after escalating to kill -9"
	else
		bad "poly_stop_server reported failure against a server that SIGKILL does end"
	fi
	# IMMEDIATELY, with nothing in between: the point is that the helper does
	# not return until it has observed the death itself.
	if pgrep -f "polyemesis -addr :$PORT" >/dev/null 2>&1; then
		bad "poly_stop_server returned while the server was still running"
		pkill -9 -f "polyemesis -addr :$PORT" 2>/dev/null
	else
		ok "the server is already gone at the moment poly_stop_server returns"
	fi
fi

step "8. poly_wait_jobs is bounded, and gives up loudly instead of hanging"
# poly_cleanup used to end its sweep with a bare `wait`, which is #179's
# mechanism -- an unbounded wait in a teardown -- in the library that all twelve
# suites share. MEASURED before the fix, on darwin: with a `sleep 300`
# backgrounded, poly_cleanup had not returned after 12 seconds, and the suite's
# own watchdog is disarmed by then (`trap 'poly_watchdog_disarm; cleanup' EXIT`),
# so nothing local bounded it at all.
#
# TWO-SIDED ON PURPOSE. The first half stages a job that will NOT finish and
# requires the helper to give up inside its bound; the second requires it to
# return success promptly when there is nothing to wait for, so a helper that
# simply always gave up would fail here.
sleep 300 &
STUCK=$!
t0=$(date +%s)
if poly_wait_jobs 2 >/dev/null 2>&1; then
	bad "poly_wait_jobs reported success while a background job was still running"
else
	ok "poly_wait_jobs gives up on a job that will not finish"
fi
elapsed=$(( $(date +%s) - t0 ))
# 5 rather than 2: the bound is 2s of polling and the shell is not a real-time
# system. The number that matters is that it is nowhere near the 300s the job
# would have taken, and nowhere near the 12s measured before the bound existed.
if [ "$elapsed" -le 5 ]; then
	ok "it gave up after ${elapsed}s, not after the job's own 300s"
else
	bad "poly_wait_jobs took ${elapsed}s against a 2s bound"
fi
# No `wait "$STUCK"` here, deliberately: this file is the guard against
# unbounded waits, and `jobs -rp` lists only RUNNING jobs, so the poll below is
# both the reap and the observation.
kill -9 "$STUCK" 2>/dev/null

if poly_wait_jobs 2 >/dev/null 2>&1; then
	ok "with nothing left to wait for it returns success"
else
	bad "poly_wait_jobs failed with no background jobs running"
fi

step "9. The teardown verdict reaches the suite's exit status"
# poly_cleanup's verdict used to be a printed opinion: it returned 1 and the
# only caller was `trap 'poly_watchdog_disarm; cleanup' EXIT`, whose status is
# discarded. A green `acceptance:` job was therefore never evidence that
# teardown succeeded -- and the next suite in the matrix is the one that pays,
# because it binds the port the last one failed to release.
#
# Driven END TO END through a real EXIT trap in a real subshell script rather
# than by calling poly_cleanup_exit directly, because the half that was missing
# was never the arithmetic -- it was the wiring: where `$?` is captured, and
# whether the trap exits with it.
#
# THE FIXTURE SOURCES THE REAL LIBRARY and overrides only poly_cleanup, so the
# precedence under test is the shipped poly_cleanup_exit and the trap line is
# the shipped one (asserted verbatim against all twelve suites in case 10).
#
# poly_watchdog_disarm is stubbed to `:`, which SETS $? TO 0 -- exactly what the
# real one does, since it ends in `kill` and `wait`. Passing `$?` as an ARGUMENT
# is what makes that harmless: the shell expands it while assembling the call,
# before anything in the trap runs. Sub-case (c) is what notices if it stops
# being expanded there.
TRAP_SHAPE="trap 'poly_teardown_trap \$? cleanup' EXIT"

WORK9="$(mktemp -d)"
# shellcheck disable=SC2016  # these single quotes are script text being WRITTEN,
# not shell to expand here.
make_suite() { # make_suite <path> <teardown-rc> <body-exit>
	local path="$1" teardown_rc="$2" body_exit="$3"
	{
		echo '#!/usr/bin/env bash'
		echo 'set -uo pipefail'
		echo ". \"$SCRIPTS/lib-cleanup.sh\""
		echo 'poly_watchdog_disarm() { :; }'
		echo "poly_cleanup() { return $teardown_rc; }"
		echo 'cleanup() { poly_cleanup_exit "${1:-0}" 19999; }'
		echo "$TRAP_SHAPE"
		echo "exit $body_exit"
	} > "$path"
	chmod +x "$path"
	return
}

suite_status() { # suite_status <teardown-rc> <body-exit>
	local teardown_rc="$1" body_exit="$2"
	make_suite "$WORK9/suite.sh" "$teardown_rc" "$body_exit"
	bash "$WORK9/suite.sh" >/dev/null 2>&1
	echo $?
}

# (a) THE CENTRAL CASE. Body passed, teardown did not: the run is not a pass.
got=$(suite_status 1 0)
if [ "$got" -ne 0 ]; then
	ok "a suite whose teardown FAILED exits non-zero (got $got)"
else
	bad "a suite exited 0 with a failed teardown -- the verdict is still a printed opinion, and a green acceptance job still means nothing about teardown"
fi

# (b) The other side of it. A gate that always failed would satisfy (a).
got=$(suite_status 0 0)
if [ "$got" -eq 0 ]; then
	ok "a suite that passed with a clean teardown still exits 0"
else
	bad "a passing suite with a clean teardown exited $got -- every run in the matrix would now be red for no reason"
fi

# (c) The expensive mistake this shape exists to avoid, twice over: a red suite
# must keep its OWN status. Reading it as 0 (captured after the disarm) turns a
# failure green; renumbering it to 1 loses which failure it was.
got=$(suite_status 0 3)
if [ "$got" -eq 3 ]; then
	ok "a suite whose body FAILED keeps its own status (3) through a clean teardown"
else
	bad "a suite that exited 3 came out as $got -- either \$? was captured after poly_watchdog_disarm clobbered it, or the teardown renumbered a product failure"
fi

# (d) And still its own, when the teardown ALSO failed. A generic teardown
# status overwriting a specific product one is the misdirection this whole
# class costs hours to.
got=$(suite_status 1 3)
if [ "$got" -eq 3 ]; then
	ok "a failed teardown does not overwrite the body's status (still 3)"
else
	bad "a suite that exited 3 came out as $got once teardown also failed -- a teardown problem is now indistinguishable from a product one"
fi
rm -rf "$WORK9"

step "10. Every suite installs that exact trap, not its own variant"
# Case 9 proves the SHAPE works. This proves the shape is what the suites
# actually carry -- thirteen files is twelve chances to drift, and a suite that
# quietly reverted to `trap 'poly_watchdog_disarm; cleanup' EXIT` would go back
# to discarding the verdict while every check above still passed.
#
# acceptance-obs-multitrack.sh was added here when it adopted the shared trap,
# and the reason it had not is the reason this list matters: it was the only
# host suite still signalling its server and returning, and running it twice
# back to back bound :1935 twice and failed. A list that does not grow with the
# suites is a list that stops being a census.
#
# Identity, not count: the offending files are named.
SUITES="acceptance.sh acceptance-audio.sh acceptance-encoders.sh acceptance-failover.sh
acceptance-mqtt.sh acceptance-playlist-phase0.sh acceptance-postprod.sh acceptance-pull.sh
acceptance-recording-stop.sh acceptance-renditions.sh acceptance-synth.sh acceptance-tls.sh
acceptance-obs-multitrack.sh"
missing=""
checked=0
for s in $SUITES; do
	if [ ! -f "$SCRIPTS/$s" ]; then
		missing="$missing $s(absent)"
		continue
	fi
	checked=$((checked + 1))
	grep -Fqx "$TRAP_SHAPE" "$SCRIPTS/$s" || missing="$missing $s"
	grep -q "poly_cleanup_exit" "$SCRIPTS/$s" || missing="$missing $s(no-poly_cleanup_exit)"
done
if [[ "$checked" -ne 13 ]]; then
	bad "only $checked of 13 named suites exist; a renamed suite would make this check pass by examining nothing"
elif [ -n "$missing" ]; then
	bad "these suites do not carry the shared trap shape:$missing"
else
	ok "all 13 suites carry the shared trap and call poly_cleanup_exit"
fi

total=$((pass + fail))
EXPECTED_CHECKS=17
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
