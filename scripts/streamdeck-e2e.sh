#!/usr/bin/env bash
# Stream Deck end to end: the plugin's Runtime against the real binary.
#
# The plugin's unit tests run against fakes and the server's own goldens
# (integrations/streamdeck/test/contract/). This runs the plugin against the
# real thing: the built polyemesis on a temporary data directory, over real
# HTTP, with real FFmpeg on both sides of a destination.
#
#   1. start the binary on loopback;
#   2. through the API, create the admin (first-run setup), an admin token and
#      a read token, a programme, and one custom RTMP destination pointing at a
#      local sink (integrations/streamdeck/test/e2e/install.mjs);
#   3. run the deck (test/e2e/deck.e2e.test.ts): pair, read the state, start
#      the destination and see it live at the sink, kill the sink and see the
#      key say Failed with its reason, retry from the key and see it live
#      again, stop it from a held stop key; then a read token's tap sends
#      nothing.
#
# The deck asserts on the plugin's computed key faces, not on HTTP statuses.
#
# Usage:  ./scripts/streamdeck-e2e.sh [parent-dir]
#         The run's files go in a new directory made inside parent-dir (default
#         $TMPDIR). Nothing that already exists is ever deleted; a run that
#         passes removes only the directory it made.
# Needs:  ./polyemesis (make build-go), ffmpeg with libx264, node, and
#         `npm ci` in integrations/streamdeck. No SRT: everything is RTMP or
#         the relay's own UDP, so Homebrew's FFmpeg is enough.
#
# Ports:  POLY_E2E_PORT (HTTP, default 18497) and POLY_E2E_SINK_PORT (the RTMP
#         sink, default 19357), both on 127.0.0.1. The server also tries its
#         default one-port ingest listeners (UDP 6000, TCP 1935); this run
#         uses neither, so a host where something else holds them is fine.
#
# Teardown stops exactly what this run started: the server by its PID, the
# children it had, and every media process carrying this run's marker. It
# never reaps another process's FFmpeg.
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
PARENT="${1:-${TMPDIR:-/tmp}}"
WORK=""
PORT="${POLY_E2E_PORT:-18497}"
SINK_PORT="${POLY_E2E_SINK_PORT:-19357}"
BIN="${POLY_E2E_BIN:-$ROOT/polyemesis}"
PLUGIN="$ROOT/integrations/streamdeck"
# On every media process this run starts, so teardown can find exactly ours.
MARKER="deck-e2e-$$"
SERVER_PID=""

# poly_require_ports. Its teardown helpers are not used: they stop servers by
# port pattern and reap any FFmpeg that appeared during the run, which on a
# developer's machine can be somebody else's.
. "$SCRIPTS/lib-cleanup.sh"
# A deadline of our own, inside the job ceiling: see lib-watchdog.sh.
. "$SCRIPTS/lib-watchdog.sh"
. "$SCRIPTS/lib-preflight.sh"

# Armed before the preflight checks, so a run that refuses still ends on a
# verdict line. The teardown trap below replaces it once there is something to
# tear down.
trap 'poly_verdict_trap $?' EXIT

step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

poly_require_exec "$BIN" "build it first: make build-go"
poly_require_cmd ffmpeg "the deck's feed and RTMP sink, and the server's own outputs"
poly_require_cmd node "the deck is the plugin's own Runtime, under vitest"
[ -x "$PLUGIN/node_modules/.bin/vitest" ] ||
	poly_preflight_fail "$PLUGIN/node_modules is not installed (cd integrations/streamdeck && npm ci)"
ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx264 ||
	poly_preflight_fail "this ffmpeg has no libx264 encoder for the synthetic feed"
poly_require_ports "$PORT/tcp" "$SINK_PORT/tcp"

# stop_pid <pid> <what>: SIGTERM, then watch it go; SIGKILL if it is still
# there after 30 s, and watch again. Returns 1 if it outlived both.
stop_pid() {
	local pid="$1" what="$2"
	[ -n "$pid" ] || return 0
	kill -0 "$pid" 2>/dev/null || return 0
	kill -TERM "$pid" 2>/dev/null
	for _ in $(seq 1 150); do
		kill -0 "$pid" 2>/dev/null || return 0
		sleep 0.2
	done
	printf "  \033[33mFINDING\033[0m  %s (pid %s) did not stop within 30s of SIGTERM; killing it\n" "$what" "$pid" >&2
	kill -KILL "$pid" 2>/dev/null
	for _ in $(seq 1 25); do
		kill -0 "$pid" 2>/dev/null || return 0
		sleep 0.2
	done
	printf "  \033[31mFAIL\033[0m  %s (pid %s) is still running after SIGKILL\n" "$what" "$pid" >&2
	return 1
}

cleanup() {
	local rc="$1" kids="" pid
	if [ -n "$SERVER_PID" ]; then
		# Its children, read before it goes: a server that dies uncleanly
		# leaves them behind, and they are this run's to stop.
		kids="$(pgrep -P "$SERVER_PID" 2>/dev/null | tr '\n' ' ')"
		stop_pid "$SERVER_PID" "the server" || rc=1
	fi
	for pid in $kids; do
		# Still an FFmpeg, the only thing the server runs here: a PID the server
		# reaped on its way out could, however unlikely, be somebody else's now.
		case "$(ps -o comm= -p "$pid" 2>/dev/null)" in
		*ffmpeg* | *ffprobe*) stop_pid "$pid" "a child of the server" || rc=1 ;;
		esac
	done
	for pid in $(pgrep -f "$MARKER" 2>/dev/null); do
		stop_pid "$pid" "a media process of this run" || rc=1
	done
	if [ "$rc" -ne 0 ] && [ -n "$WORK" ] && [ -s "$WORK/server.log" ]; then
		printf "\n--- the last of server.log ---\n"
		tail -n 60 "$WORK/server.log"
		printf "\nthe run's files are kept in %s\n" "$WORK"
	elif [ "$rc" -eq 0 ] && [ -n "$WORK" ]; then
		# Only the directory mktemp made for this run.
		rm -rf "$WORK"
	fi
	return "$rc"
}
trap 'poly_teardown_trap $? cleanup' EXIT

mkdir -p "$PARENT" || exit 1
WORK="$(mktemp -d "$PARENT/polyemesis-streamdeck-e2e.XXXXXX")" || exit 1
cd "$WORK" || exit 1
# Five minutes: the run takes about one, and this fires before the CI step's
# own timeout, so a hang names what it was waiting for.
poly_watchdog_arm 300

step "1. Start the binary on 127.0.0.1:$PORT"
"$BIN" -addr "127.0.0.1:$PORT" -data "$WORK/data" -log warn >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

step "2. Create the admin, the tokens, a programme and its destination, through the API"
node "$PLUGIN/test/e2e/install.mjs" "http://127.0.0.1:$PORT" "$WORK/install.env" \
	"rtmp://127.0.0.1:$SINK_PORT/deck/$MARKER" || exit 1
# shellcheck source=/dev/null
. "$WORK/install.env"
export POLY_E2E_MARKER="$MARKER"

step "3. The deck: pair, read, start, fail, retry, stop"
(cd "$PLUGIN" && ./node_modules/.bin/vitest run --config vitest.e2e.config.ts --reporter=verbose)
