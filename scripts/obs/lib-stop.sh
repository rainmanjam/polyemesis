#!/usr/bin/env bash
# poly_bounded_stop -- ask a process to die, then OBSERVE that it did.
#
# WHY THIS IS A SEPARATE FILE
#
# It is the last two lines of scripts/obs/entrypoint.sh, and those two lines had
# both halves of the termination defect at once (#208):
#
#   obs --startstreaming ... 2>&1 | sed 's/^/  [obs] /' &
#   OBS_PID=$!                      # <- the pid of SED, not of obs
#   sleep "$SECONDS_TO_STREAM"
#   kill -TERM "$OBS_PID" ...       # <- so this signals the log prefixer
#   wait "$OBS_PID" ...             # <- and this is unbounded
#
# `$!` after a PIPELINE is the last element. So the TERM went to `sed`, sed died,
# OBS carried on with its stdout closed, and the `wait` returned as soon as sed
# was reaped. That is why this never visibly hung -- and also why it never
# actually stopped OBS. The container's own teardown did.
#
# Fixing the pid without bounding the wait would have made the latent half live:
# a real SIGTERM to a GUI application under Xvfb with a wedged encoder or a stuck
# RTMP socket is exactly the case where the signal is not honoured promptly, and
# the unbounded `wait` behind it is #179's body verbatim -- 28 minutes of CI and
# a log that came back BlobNotFound.
#
# It lives here rather than inline so it can be TESTED. The container needs OBS,
# Xvfb, Mesa and PulseAudio; this function needs a pid. scripts/test-obs-stop.sh
# drives it with `sleep` and with a SIGTERM-deaf stand-in, which is the whole
# reason the fix is not another blind edit to an entrypoint nobody ran.
#
# lib-cleanup.sh is NOT reused here, deliberately: it is host-side, it is built
# around pkill patterns and lsof port holders, and neither lsof nor the suite's
# process names exist inside the OBS image. This is the same discipline against a
# pid.

# poly__alive <pid> -- is that pid a RUNNING process, as opposed to gone or a
# zombie?
#
# THE ZOMBIE BRANCH IS DEFENSIVE, and is labelled as such rather than justified
# with a story. `kill -0` answers SUCCESS for a zombie, and a poll built on it
# alone would watch a process that has already exited, exhaust the grace period,
# SIGKILL a corpse and report "still running after SIGKILL" -- a loud failure
# invented entirely by the observer.
#
# For THIS caller it is unreachable, and that was measured rather than assumed:
# bash reaps its own background children into its jobs table, so by the time the
# poll runs on an exited child, `kill -0` already returns 1 and `ps -o state=`
# prints nothing (darwin, bash 3.2). It earns its place only if this is ever
# pointed at a pid that is not this shell's child -- a pid read from a file, say.
# scripts/test-obs-stop.sh says out loud that it does not cover it.
poly__alive() {
	kill -0 "$1" 2>/dev/null || return 1
	case "$(ps -o state= -p "$1" 2>/dev/null | tr -d ' ')" in
	Z* | "") return 1 ;;
	esac
	return 0
}

# poly_bounded_stop <pid> [label] [term-secs] [kill-secs]
#
# TERM, observe for term-secs, escalate to KILL, observe again for kill-secs,
# and say so loudly if it is STILL there. The same shape poly_stop_server uses
# host-side, for the same reason: a signal is a request, and on every platform
# there is an interval between asking a process to die and observing it dead.
#
# Returns 0 once the process is observed gone, 1 if it outlived SIGKILL. It
# never blocks longer than term-secs + kill-secs.
poly_bounded_stop() {
	local pid="$1" label="${2:-process}" term_secs="${3:-15}" kill_secs="${4:-5}"
	[ -n "$pid" ] || return 0

	# Already gone, or never started. Not an error, and worth returning before
	# signalling: the pid may have been recycled by now.
	poly__alive "$pid" || {
		poly__reap "$pid"
		return 0
	}

	kill -TERM "$pid" 2>/dev/null
	for _ in $(seq 1 $((term_secs * 4))); do
		poly__alive "$pid" || {
			poly__reap "$pid"
			return 0
		}
		sleep 0.25
	done

	printf '  [%s] still running %ss after SIGTERM; escalating to SIGKILL\n' \
		"$label" "$term_secs" >&2
	kill -9 "$pid" 2>/dev/null
	for _ in $(seq 1 $((kill_secs * 4))); do
		poly__alive "$pid" || {
			poly__reap "$pid"
			return 0
		}
		sleep 0.25
	done

	printf '  [%s] FAIL: pid %s is STILL running %ss after SIGKILL.\n' \
		"$label" "$pid" "$kill_secs" >&2
	printf '  [%s]       Whatever it holds -- an RTMP socket, a recording, a\n' "$label" >&2
	printf '  [%s]       port -- it still holds, and anything that reads those\n' "$label" >&2
	printf '  [%s]       next is reading a machine in a state nobody observed.\n' "$label" >&2
	return 1
}

# poly__reap <pid> -- collect the corpse.
#
# This `wait` is bounded BY CONSTRUCTION and not by a timer: it is only ever
# reached after poly__alive has said the process is gone or a zombie, so there
# is nothing left to block on. That ordering -- observe, then wait -- is the
# entire difference between this and the line it replaces.
poly__reap() {
	wait "$1" 2>/dev/null || true
	return 0
}
