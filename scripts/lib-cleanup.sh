#!/usr/bin/env bash
# Shared teardown for the host acceptance suites.
#
# WHY THIS EXISTS
#
# The supervisor puts every FFmpeg child in its own process group on purpose,
# so that a Ctrl-C in the terminal reaches the server first instead of killing
# children out from under it. The cost is that killing the SERVER alone orphans
# them. They keep running, keep holding UDP ports in the relay allocator's
# range, and keep publishing into ports that a LATER run then allocates for
# something else. Two streams arrive on one port, the consumer logs corrupt
# packets and impossible timestamp jumps, and the failure looks like a product
# bug in whichever suite happens to run next.
#
# That is not hypothetical. It reached 74 stray processes and a load average of
# 14 before anyone noticed, cost hours of misdirected debugging, and is the most
# likely explanation for the "flaky under load" behaviour seen in the postprod
# suite -- which was investigated as a product regression and was not one.
#
# USAGE
#
#   source "$(dirname "$0")/lib-cleanup.sh"
#   trap 'poly_cleanup "$PORT" "$WORK"' EXIT
#
# WORK is optional; pass it when the suite has a working directory, so the
# backstop sweep can identify this run's processes by their command line.

# poly_stop_server signals one server and WAITS for it to go.
#
# The wait is the part that matters. A graceful shutdown finalises recordings
# and stops each child itself, which is the only teardown that leaves nothing
# behind -- but it measured 8.3s, and a script that signals and exits
# immediately never gives it the chance.
poly_stop_server() {
  local port="$1"
  pkill -f "polyemesis -addr :$port" 2>/dev/null
  local i
  for i in $(seq 1 30); do
    pgrep -f "polyemesis -addr :$port" >/dev/null 2>&1 || return 0
    sleep 0.5
  done
  pkill -9 -f "polyemesis -addr :$port" 2>/dev/null
}

# poly_cleanup stops the server(s) and reports anything that outlived them.
#
# The stray check is source inspection rather than a broader kill: a sweep wide
# enough to catch every orphan is also wide enough to kill a suite running in
# another terminal. Reporting makes the leak visible, which is the one thing it
# was not before.
poly_cleanup() {
  local ports="$1" work="${2:-}"
  local p
  for p in $ports; do
    poly_stop_server "$p"
  done

  if [ -n "$work" ]; then
    # Ours by definition: no other suite has this path on its command line.
    pkill -9 -f "ffmpeg.*$work" 2>/dev/null
  fi
  wait 2>/dev/null

  local left=0
  if [ -n "$work" ]; then
    left=$(pgrep -f "ffmpeg.*$work" 2>/dev/null | wc -l | tr -d ' ')
  fi
  if [ "${left:-0}" -gt 0 ]; then
    printf "  \033[33mWARN\033[0m  %s stray ffmpeg process(es) survived teardown.\n" "$left"
    printf "        They hold relay ports and will corrupt the next run.\n"
    printf "        Clear them with: pkill -9 -x ffmpeg\n"
  fi
}
