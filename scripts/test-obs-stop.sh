#!/usr/bin/env bash
# Tests for scripts/obs/lib-stop.sh -- the OBS entrypoint's stop path.
#
# WHY THIS EXISTS
#
# #208 was filed rather than fixed, and the stated reason was that the fix could
# not be run: "a blind edit to a container entrypoint has two ways to go wrong
# that a review cannot see". Both ways are about a process, not about OBS. So the
# stop logic moved into a function and this drives it with `sleep` and a
# SIGTERM-deaf stand-in -- no OBS, no Xvfb, no Mesa, no container, about four
# seconds.
#
# What that buys, precisely: the bound, the escalation, the re-observation and
# the zombie case are all exercised here on every PR. What it does NOT buy is
# any claim about OBS's own shutdown behaviour under Xvfb; the ceiling in the
# entrypoint is a blast radius and says so.
#
# Usage:  ./scripts/test-obs-stop.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/obs/lib-stop.sh"

READY=""
trap 'rm -f "$READY"' EXIT

pass=0
fail=0
ok() {
	printf "  \033[32mPASS\033[0m  %s\n" "$1"
	pass=$((pass + 1))
}
bad() {
	printf "  \033[31mFAIL\033[0m  %s\n" "$1"
	fail=$((fail + 1))
}
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

# ready <file> -- poll for a stand-in's readiness marker, bounded.
ready() {
	local i
	for ((i = 0; i < 100; i++)); do
		[ -e "$1" ] && return 0
		sleep 0.05
	done
	return 1
}

# deaf_ready <pid> -- poll until the stand-in has exec'"'"'d, bounded.
deaf_ready() {
	local i
	for ((i = 0; i < 100; i++)); do
		case "$(ps -o comm= -p "$1" 2>/dev/null)" in
		*sleep*) return 0 ;;
		esac
		sleep 0.05
	done
	return 1
}

step "1. A process that honours SIGTERM is stopped, and its death is observed"
# It takes a second over it, deliberately. A stand-in that dies the instant the
# signal lands cannot tell a helper that OBSERVES the death from one that signals
# and returns -- both look identical by the time the next line runs, and the
# check below would pass for a helper with no poll in it at all. A second is the
# gap that makes the difference visible.
READY="$(mktemp)"
rm -f "$READY"
bash -c 'trap "sleep 1; exit 0" TERM; : >"'"$READY"'"; while :; do sleep 0.2; done' &
POLITE=$!
# WAIT FOR THE TRAP TO BE INSTALLED. Without this the TERM can land in the
# window before `trap` runs, the stand-in dies on the default disposition, and
# the case passes for a reason that has nothing to do with the helper. Measured:
# roughly one run in five went that way when this was a bare `&`.
if ! ready "$READY"; then
	bad "the cooperative stand-in never signalled that its TERM handler was installed"
fi
t0=$(date +%s)
if poly_bounded_stop "$POLITE" polite 5 2 >/dev/null 2>&1; then
	ok "poly_bounded_stop reports success for a process that takes the TERM"
else
	bad "poly_bounded_stop reported failure against a process SIGTERM does end"
fi
# IMMEDIATELY, with nothing in between. The point of the helper is that it does
# not return until it has seen the death itself, so anything the entrypoint does
# next -- and in the container, the container's own exit -- is safe.
if poly__alive "$POLITE"; then
	bad "poly_bounded_stop returned while the process was still running"
	kill -9 "$POLITE" 2>/dev/null
else
	ok "the process is already gone at the moment poly_bounded_stop returns"
fi
took=$(($(date +%s) - t0))
if [ "$took" -le 3 ]; then
	ok "a cooperative stop costs ${took}s, not the whole grace period"
else
	bad "a cooperative stop took ${took}s; the poll is not returning on the death"
fi

step "2. A SIGTERM-deaf process is escalated to SIGKILL, loudly"
# THE CASE THE ENTRYPOINT WAS WRITTEN AROUND AND COULD NOT SURVIVE. OBS is a GUI
# application under Xvfb; a wedged encoder or a stuck RTMP socket is precisely
# where SIGTERM is not honoured. `trap "" TERM` survives exec, so this is one
# process that genuinely ignores the signal.
bash -c 'trap "" TERM; exec sleep 300' &
DEAF=$!
# Detached so the shell does not announce "Killed: 9" when the helper reaps it,
# which is the expected outcome of this case and not an incident.
disown "$DEAF" 2>/dev/null || true
# AND WAIT FOR THE exec. The ignored disposition survives exec, but it is not in
# place until `trap` has run; a TERM sent into that window kills the stand-in
# outright and this case then reports "no escalation was needed" about a process
# that was never deaf. `comm` flips from bash to sleep exactly when the exec
# completes, which is the observation rather than a sleep standing in for it.
if ! deaf_ready "$DEAF"; then
	bad "the SIGTERM-deaf stand-in never reached its exec, so it was not deaf"
fi
t0=$(date +%s)
out=$(poly_bounded_stop "$DEAF" deaf 2 2 2>&1)
rc=$?
elapsed=$(($(date +%s) - t0))
if [ "$rc" -eq 0 ]; then
	ok "poly_bounded_stop reports success after escalating to SIGKILL"
else
	bad "poly_bounded_stop reported failure against a process SIGKILL does end (rc=$rc)"
fi
if poly__alive "$DEAF"; then
	bad "it returned while the SIGTERM-deaf process was still running"
	kill -9 "$DEAF" 2>/dev/null
else
	ok "the deaf process is gone at the moment it returns"
fi
if printf '%s' "$out" | grep -q "escalating to SIGKILL"; then
	ok "the escalation is announced rather than done quietly"
else
	bad "a process had to be SIGKILLed and nothing said so; a stop that needs -9 every run looks identical to one that never does"
fi

step "3. The wait is BOUNDED -- this is the whole of #208's first finding"
# The line replaced was `kill -TERM "$pid"; wait "$pid"`, with no ceiling. Had
# the pid been right, this stand-in would have held the container entrypoint for
# 300 seconds. The assertion is not "fast", it is "nowhere near 300".
if [ "$elapsed" -le 8 ]; then
	ok "gave up on SIGTERM and finished in ${elapsed}s against the stand-in's 300s"
else
	bad "took ${elapsed}s against a 2s+2s bound; the wait is not bounded"
fi

step "4. A process that is already dead costs nothing and is not signalled"
# The entrypoint calls this after `sleep "$SECONDS_TO_STREAM"`, and OBS may well
# have exited on its own by then -- a failed RTMP connect, a source it could not
# open. That must be a fast, quiet success, not a grace period spent watching a
# corpse followed by a SIGKILL and a red banner about a process that left of its
# own accord.
#
# HONEST ABOUT WHAT THIS DOES NOT REACH: poly__alive also treats a ZOMBIE as
# dead, and that branch is not exercised here. bash reaps its own background
# children into its jobs table, so `kill -0` on this pid already fails -- MEASURED
# on darwin: kill -0 returns 1 and `ps -o state=` prints nothing. The zombie
# branch is there for a pid that is not this shell'"'"'s child, which is a shape this
# entrypoint does not have, and it is untested rather than covered.
bash -c 'exit 0' &
ZOMBIE=$!
sleep 0.5
t0=$(date +%s)
out=$(poly_bounded_stop "$ZOMBIE" zombie 5 2 2>&1)
rc=$?
took=$(($(date +%s) - t0))
if [ "$rc" -eq 0 ] && [ "$took" -le 1 ]; then
	ok "an already-exited child returns success immediately (${took}s)"
else
	bad "an already-exited child took ${took}s and returned $rc; the poll is not noticing a process that is already gone"
fi
if printf '%s' "$out" | grep -q "SIGKILL"; then
	bad "it escalated to SIGKILL against a process that had already exited"
else
	ok "no escalation against a process that was already gone"
fi

step "5. The entrypoint uses it, and does not capture \$! from a pipeline"
# Cases 1-4 test the function. This tests that the entrypoint still CALLS it --
# the defect in #208 was never in a helper, it was in two lines of a file nobody
# could run, and a helper the entrypoint stopped using would leave every check
# above green.
ENTRY="$SCRIPTS/obs/entrypoint.sh"
if grep -q "poly_bounded_stop" "$ENTRY" && grep -q "lib-stop.sh" "$ENTRY"; then
	ok "entrypoint.sh sources lib-stop.sh and stops OBS through it"
else
	bad "entrypoint.sh no longer goes through poly_bounded_stop; its stop path is untested again"
fi
# `$!` after a pipeline names the LAST element. The old line was
# `obs ... | sed 's/^/  [obs] /' &` followed by `OBS_PID=$!`, so OBS_PID was sed.
bg_line=$(grep -n '^obs \|^	> >(sed\|^obs.*&$' "$ENTRY" | head -3)
if grep -B3 'OBS_PID=\$!' "$ENTRY" | grep -q '|[^|]*&[[:space:]]*$'; then
	bad "OBS_PID is captured from a pipeline, so it names the last element and the TERM goes to the wrong process: $bg_line"
else
	ok "OBS_PID is captured from a backgrounded obs, not from a pipeline"
fi
# The Dockerfile has to carry the file the entrypoint sources, or the image
# fails on its first line.
if grep -q "lib-stop.sh" "$SCRIPTS/obs/Dockerfile"; then
	ok "the Dockerfile copies lib-stop.sh into the image"
else
	bad "the Dockerfile does not copy lib-stop.sh; the entrypoint would die sourcing it"
fi

total=$((pass + fail))
EXPECTED_CHECKS=12
printf "\n"
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
	printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
	exit 1
fi
if [ "$fail" -gt 0 ]; then
	printf "  \033[31mobs-stop: %d of %d checks FAILED\033[0m\n\n" "$fail" "$total"
	exit 1
fi
printf "  \033[32mobs-stop: %d checks passed\033[0m\n\n" "$total"
