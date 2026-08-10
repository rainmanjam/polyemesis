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
# THE WORK-DIR SWEEP HAS A HOLE, and a 20-run measurement found it. The sweep
# below kills `ffmpeg.*$work`, which matches any child whose argv carries a path
# under the working directory -- the recorder, the clipper, the playout muxer.
# It does NOT match the INGEST, whose argv is only
# `-i rtmp://0.0.0.0:<port>/live ... udp://127.0.0.1:<relay>`: no work-dir path
# appears anywhere in it. So the ingest survived teardown, kept its listen port,
# and the leftover COUNT missed it too because it used the same pattern -- the
# leak was both unkilled and invisible.
#
# Measured consequence: acceptance-failover failed 10 runs out of 20 back to
# back, every failure with "Address already in use" on the ingest port and a
# 43-second duration against a 40-second ceiling. It alternated almost perfectly
# -- a run leaked the port, the next died fast, its quick death leaked less, and
# the one after that passed.
#
# USAGE
#
#   source "$(dirname "$0")/lib-cleanup.sh"
#   trap 'poly_cleanup "$PORT" "$WORK" "$INGEST"' EXIT
#
# WORK is optional; pass it when the suite has a working directory, so the
# backstop sweep can identify this run's processes by their command line.
#
# The third argument is optional and is the list of ports the NEXT run has to be
# able to bind -- an ingest listener, typically. Pass it whenever the suite binds
# a fixed port that is not the server's own, because that is the one the work-dir
# sweep cannot see.

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

  # THE LAST STEP WAS MISSING: this used to send -9 and return, which asserted
  # nothing. A signal is a request, and on every platform there is an interval
  # between asking a process to die and observing it dead -- that is the whole
  # subject of #179/#180. Even SIGKILL is not instant: the kernel still has to
  # tear down the address space, and a process wedged in uninterruptible I/O
  # does not leave until it returns.
  #
  # So re-observe, exactly as poly_free_port below already does. The bound is
  # 3s, generous against a measured teardown of well under one, and the failure
  # is LOUD: a server that survived -9 will hold its port and its recordings,
  # and the next thing that happens is a caller acting as though it were gone.
  pkill -9 -f "polyemesis -addr :$port" 2>/dev/null
  for _ in $(seq 1 12); do
    pgrep -f "polyemesis -addr :$port" >/dev/null 2>&1 || return 0
    sleep 0.25
  done
  printf "  \033[31mFAIL\033[0m  the server on port %s is STILL running after kill -9.\n" "$port"
  printf "        It holds the port and whatever recordings it had open, and every\n"
  printf "        check that follows is about to be run against a machine that is\n"
  printf "        not in the state this teardown reported.\n"
  return 1
}

# poly_port_holders prints the PIDs holding a port, or nothing.
#
# lsof rather than ss or netstat: it is the one tool present on both the macOS
# developer machines and the Linux runners, and ci.yml already installs it for
# the suites that read relay ports out of it.
poly_port_holders() {
	local port="$1"
	[ -n "$port" ] || return 0
	command -v lsof >/dev/null 2>&1 || return 0
	lsof -ti ":$port" 2>/dev/null
}

# poly_wait_port_ready waits for something to START listening on a port.
#
# The mirror image of poly_free_port, and it lives here because it needs the
# same poly_port_holders. This file is now about ports and processes rather than
# strictly teardown; the name has not caught up.
#
# It exists because "the server said it is ready" is not "the ingest is
# accepting connections". The server logs its web UI and the suite proceeds,
# while the ingest child is still being spawned and has not bound its listen
# socket yet. A publisher that arrives in that window gets Connection refused
# and exits immediately, and the suite then waits out its full ceiling for a
# primary that was never going to arrive.
#
# Measured at roughly 1 run in 20 before this existed -- the entire residual
# flake rate of acceptance-failover once the port leak was fixed.
poly_wait_port_ready() {
	local port="$1" secs="${2:-15}"
	[ -n "$port" ] || return 0
	local deadline=$(( $(date +%s) + secs ))
	while [ "$(date +%s)" -lt "$deadline" ]; do
		[ -n "$(poly_port_holders "$port")" ] && return 0
		sleep 0.1
	done
	printf "  \033[33mWARN\033[0m  nothing is listening on port %s after %ss.\n" "$port" "$secs"
	printf "        A publisher started now would be refused, and the wait that\n"
	printf "        follows would blame the source for never going on air.\n"
	return 1
}

# poly_free_port waits for a port to be released, then takes it back by force.
#
# The wait comes first because a graceful teardown does release it, just not
# instantly: the supervisor signals each child and the kernel needs a moment to
# reclaim the socket. Killing immediately would hide a teardown that works.
#
# The kill is the backstop, and it is safe to be blunt here for a reason the
# work-dir sweep cannot claim: the server for this run is already gone by the
# time this is called, so anything still holding a port this suite owns is this
# suite's orphan. A wide `pkill -x ffmpeg` would not be safe; this is, because
# the predicate is a specific port rather than a program name.
poly_free_port() {
	local port="$1" pids
	[ -n "$port" ] || return 0

	for _ in $(seq 1 20); do
		[ -z "$(poly_port_holders "$port")" ] && return 0
		sleep 0.25
	done

	pids=$(poly_port_holders "$port")
	[ -z "$pids" ] && return 0
	printf "  \033[33mWARN\033[0m  port %s still held 5s after teardown; reclaiming it.\n" "$port"
	printf "        The next run would have failed to bind with \"Address already in use\".\n"
	# shellcheck disable=SC2086
	kill -9 $pids 2>/dev/null
	for _ in $(seq 1 12); do
		[ -z "$(poly_port_holders "$port")" ] && return 0
		sleep 0.25
	done
	printf "  \033[31mFAIL\033[0m  port %s is STILL held after kill -9.\n" "$port"
	return 1
}

# poly_wait_jobs waits for this shell's background jobs, BOUNDED.
#
# It replaces a bare `wait` in poly_cleanup, which was #179's mechanism sitting
# in the shared teardown of all twelve suites: an unbounded wait in a teardown,
# in the library that both of the CI steps #179 fixed are conceptually
# downstream of.
#
# The bound is not belt-and-braces. Every suite traps
# `poly_watchdog_disarm; cleanup`, so the suite's OWN deadline is already off by
# the time this runs; locally nothing else bounds it at all, and in CI only the
# `acceptance` job's step timeout does. MEASURED on darwin before this existed:
# with a `sleep 300` backgrounded, poly_cleanup had not returned after 12s.
#
# Polling `jobs -rp` rather than `wait -n` with a timeout, because `wait -n`
# needs bash 4.3+ and the developer machines run Apple's bash 3.2. Reading
# `jobs` is also what reaps a finished child, so this is a real wait and not
# merely a sleep.
#
# Giving up is LOUD and then continues: the remaining sweeps below are exactly
# what a leftover job needs, so refusing to run them would be the wrong answer
# to finding one.
poly_wait_jobs() {
	local secs="${1:-5}" left
	for _ in $(seq 1 $((secs * 4))); do
		left=$(jobs -rp 2>/dev/null)
		[ -z "$left" ] && return 0
		sleep 0.25
	done
	left=$(jobs -rp 2>/dev/null | tr '\n' ' ')
	[ -z "$left" ] && return 0
	printf "  \033[33mWARN\033[0m  background job(s) still running %ss into teardown: %s\n" "$secs" "$left"
	printf "        Not waiting any longer -- an unbounded wait here is the defect\n"
	printf "        #179 cost 28 minutes of CI to. The sweeps below still run.\n"
	return 1
}

# poly_cleanup stops the server(s) and reports anything that outlived them.
#
# The stray check is source inspection rather than a broader kill: a sweep wide
# enough to catch every orphan is also wide enough to kill a suite running in
# another terminal. Reporting makes the leak visible, which is the one thing it
# was not before.
poly_cleanup() {
  local ports="$1" work="${2:-}" bindports="${3:-}"
  local p stopfail=0
  for p in $ports; do
    # The return value is READ. poly_stop_server's loud failure -- "the server
    # is STILL running after kill -9" -- used to be discarded here, by its only
    # production caller, which made it a printed opinion by the standard
    # flake-report.sh:43 sets for itself. Aggregated below into one banner,
    # because a teardown that failed changes what every check after it means.
    poly_stop_server "$p" || stopfail=$((stopfail + 1))
  done

  if [ -n "$work" ]; then
    # Ours by definition: no other suite has this path on its command line.
    pkill -9 -f "ffmpeg.*$work" 2>/dev/null
  fi
  poly_wait_jobs 5

  # AFTER the work-dir sweep, because that is what releases the ports a
  # well-behaved teardown was always going to release. What is left here is
  # what the sweep cannot see.
  local portfail=0
  for p in $bindports; do
    poly_free_port "$p" || portfail=$((portfail + 1))
  done

  local left=0
  if [ -n "$work" ]; then
    left=$(pgrep -f "ffmpeg.*$work" 2>/dev/null | wc -l | tr -d ' ')
  fi
  if [ "${left:-0}" -gt 0 ]; then
    printf "  \033[33mWARN\033[0m  %s stray ffmpeg process(es) survived teardown.\n" "$left"
    printf "        They hold relay ports and will corrupt the next run.\n"
    printf "        Clear them with: pkill -9 -x ffmpeg\n"
  fi

  # ONE VERDICT FOR THE TEARDOWN, and it is deliberately a RETURN and not an
  # `exit`.
  #
  # Every suite installs this as `trap '... cleanup' EXIT`, and an EXIT trap that
  # exits REPLACES the script's own exit status. Making teardown failures fail
  # the suite from here would therefore also let a teardown hiccup overwrite a
  # green run's status and, worse, overwrite a RED one -- turning a specific
  # product failure into a generic teardown failure. Wiring the verdict through
  # to the exit status needs each suite's own trap to combine it with $?, which
  # is twelve edits and its own review. Tracked; see the issue linked from the
  # PR that added this. Until then this is honest about being a report.
  if [ "$stopfail" -gt 0 ] || [ "$portfail" -gt 0 ]; then
    printf "  \033[31mFAIL\033[0m  teardown did not complete: %s server(s) survived kill -9, %s port(s) still held.\n" "$stopfail" "$portfail"
    printf "        Everything this run reported after the point of failure was\n"
    printf "        measured against a machine that is not in the state the\n"
    printf "        teardown claimed. Treat the results as unsound, not as flaky.\n"
    return 1
  fi
  return 0
}
