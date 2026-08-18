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
#   cleanup() { poly_cleanup_exit "${1:-0}" "$PORT" "$WORK" "$INGEST"; }
#   trap 'poly_teardown_trap $? cleanup' EXIT
#
# The trap is that exact shape in all twelve suites; see poly_cleanup_exit at the
# foot of this file for why each piece of it is where it is.
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

# poly_port_bound reports whether anything is listening on port/proto.
#
# NOT poly_port_holders, which is lsof and TCP-only. An SRT ingest is UDP, and
# lsof is absent on a stock server install -- poly_port_holders returns nothing
# there, which reads as "free" and is the wrong answer to fail open on. `ss` is
# present on anything with systemd, which is every host these suites run on.
poly_port_bound() {
	local port="$1" proto="${2:-tcp}"
	command -v ss >/dev/null 2>&1 || return 1
	if [ "$proto" = udp ]; then
		ss -lnu 2>/dev/null | grep -qE "[:.]${port}\b"
	else
		ss -lnt 2>/dev/null | grep -qE "[:.]${port}\b"
	fi
}

# poly_socket_owner names the process listening on port/proto, or prints nothing.
#
# TRIES sudo -n SECOND, and that is the whole reason this is a function. ss only
# attributes a socket it has the privilege to see, so an unprivileged run
# against a service started by root reports the port as taken by nobody in
# particular -- which is exactly the case these suites hit, because the thing
# holding the port is nearly always the installed polyemesis. Non-interactive,
# so a host without passwordless sudo degrades to the anonymous message rather
# than stopping to ask for a password nobody is there to type.
poly_socket_owner() {
	local port="$1" proto="$2" flag out
	[ "$proto" = udp ] && flag=lnpu || flag=lnpt
	out="$(ss -"$flag" 2>/dev/null | grep -E "[:.]${port}\\b" | head -1)"
	case "$out" in
		*users:*) ;;
		*) out="$(sudo -n ss -"$flag" 2>/dev/null | grep -E "[:.]${port}\\b" | head -1)" ;;
	esac
	printf '%s' "$out" | grep -oE 'users:\(\("[^"]+"' | head -1 | grep -oE '"[^"]+"' | tr -d '"'
}

# poly_require_ports refuses to start when something already holds a port this
# suite is about to bind or map into a container.
#
# WHY THIS IS WORTH A HELPER. acceptance-multisource maps 6000/udp, which is the
# DEFAULT SRT ingest port -- so on any host already running polyemesis, the
# container cannot bind it and the suite failed with:
#
#	FAIL  container never became healthy
#
# That sentence is true and useless. It cost four steps of digging to reach "the
# service you are running holds the port", which the machine knew all along.
# install.sh has done this check since it shipped (port_in_use / warn_if_taken);
# the suites never borrowed it.
#
# Names the holder where the kernel will say, because "6000/udp is taken" and
# "the polyemesis you are running is on 6000/udp" lead to different next moves.
#
# Usage: poly_require_ports 8097/tcp 6000/udp 6001/udp
poly_require_ports() {
	local spec port proto taken=0 who
	for spec in "$@"; do
		port="${spec%%/*}"
		proto="${spec#*/}"
		[ "$proto" = "$spec" ] && proto=tcp
		poly_port_bound "$port" "$proto" || continue
		if [ "$taken" -eq 0 ]; then
			printf "\n\033[31mCannot start: a port this suite needs is already in use.\033[0m\n" >&2
		fi
		taken=1
		who="$(poly_socket_owner "$port" "$proto")"
		if [ -n "$who" ]; then
			printf "  %s/%s is held by \033[1m%s\033[0m\n" "$proto" "$port" "$who" >&2
		else
			printf "  %s/%s is held by another process\n" "$proto" "$port" >&2
		fi
	done
	[ "$taken" -eq 0 ] && return 0

	printf "\n  These are the real defaults, so the usual cause is a polyemesis\n" >&2
	printf "  already running on this host. Two instances cannot share an ingest\n" >&2
	printf "  port. Stop the service for the duration of the run:\n\n" >&2
	printf "      sudo systemctl stop polyemesis\n" >&2
	printf "      %s\n" "${0##*/}" >&2
	printf "      sudo systemctl start polyemesis\n\n" >&2
	exit 1
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
# POLY_FFMPEG_BASELINE is the set of ffmpeg pids that were ALREADY running when
# this library was sourced, which is before the suite has started anything.
#
# IT EXISTS SO THE CHECK BELOW CAN FAIL RATHER THAN MERELY PRINT. A bare count of
# ffmpeg on the host cannot tell a process this suite leaked from a developer's
# own transcode, or from a sibling suite running in parallel -- and a teardown
# check that goes red for someone else's process is one people learn to ignore,
# which is the state this was already in. Diffing against the baseline attributes
# the leak, so the failure names only processes this run is responsible for.
# poly_ffmpeg_pids is the ONE place the process list is obtained, so a test can
# substitute it. Faking a real process is not portable enough to rely on -- a copy
# of a system binary renamed to ffmpeg is refused outright on macOS, where the
# copy loses its code signature -- and the logic worth testing here is the
# attribution, not whether pgrep can match a name.
poly_ffmpeg_pids() { pgrep -x ffmpeg 2>/dev/null; }

# `|| true` because pgrep exits non-zero when it matches NOTHING, which is the
# ordinary case on a clean host. Without it, sourcing this library from a shell
# with `set -e` inherited would exit at this line -- and would do so precisely
# when there was nothing wrong. Found by review, not by a failing run.
POLY_FFMPEG_BASELINE="$(poly_ffmpeg_pids | tr '\n' ' ' || true)"

# poly_report_orphans names the media children that outlived the run, and FAILS
# when any of them are this run's doing.
#
# WHY THE LEAK EXISTS AT ALL. setProcessGroup puts every child in
# its OWN process group, deliberately, so a Ctrl-C reaches polyemesis and lets
# it shut its children down in order. The cost is that an ABRUPT death of
# polyemesis -- SIGKILL, an OOM, a cancelled CI job -- signals nothing, and every
# ffmpeg it started keeps running. Windows has a backstop for exactly this
# (job_windows.go, KILL_ON_JOB_CLOSE, whose own warning says "FFmpeg children
# will survive a polyemesis crash"); Linux has none.
#
# The Linux equivalent is Pdeathsig, and it is NOT applied here on purpose: in Go
# it fires when the parent THREAD exits rather than the process, so a runtime
# thread retirement could kill live destinations. That trade needs its own
# testing, not a line in a teardown helper. See #448.
#
# So this does not fix the leak -- under systemd nothing needs to, KillMode=mixed
# SIGKILLs the whole cgroup when the unit stops for any reason. What leaks is
# every OTHER way polyemesis runs: these suites (as the login user, not the root
# service), a foreground developer run, a container with a shell entrypoint. That
# is exactly where the thirteen came from.
#
# IT FAILS THE SUITE NOW, where it used to print. Thirteen of these had been
# running for up to 5.5 days on a dev box before anyone looked, and a cancelled CI
# job left a polyemesis and five ffmpeg behind. Neither said anything at the time.
# A finding nobody is forced to read is a finding nobody reads.
poly_report_orphans() {
	command -v pgrep >/dev/null 2>&1 || return 0
	local pid new="" left="" waited=0
	# In ticks of 0.1s. A seam so the library's own tests do not have to spend the
	# full settle window twice to assert on what happens after it.
	local ticks="${POLY_ORPHAN_SETTLE_TICKS:-150}"

	for pid in $(poly_ffmpeg_pids); do
		case " $POLY_FFMPEG_BASELINE " in
			*" $pid "*) continue ;;
		esac
		new="$new $pid"
	done
	[ -n "$new" ] || return 0

	# SETTLE BEFORE JUDGING, and this is not politeness -- without it the check
	# reports its own race. poly_stop_server waits for POLYEMESIS to exit, and the
	# instant it does its children reparent to init. A ppid of 1 therefore means
	# "the parent is already gone", NOT "this one is abandoned": an encoder still
	# flushing and finalising its output looks identical from outside.
	# acceptance-postprod runs transcodes and was failed by exactly that race on
	# the first run of this check, having passed all twelve of its own assertions.
	#
	# 15s is above the supervisor's own shutdownGrace (8s) plus its drain (2s), so
	# anything still here afterwards has outlived every budget the product gives
	# itself and is genuinely abandoned.
	while [ "$waited" -lt "$ticks" ]; do
		left=""
		for pid in $new; do
			if kill -0 "$pid" 2>/dev/null; then left="$left $pid"; fi
		done
		if [ -z "$left" ]; then return 0; fi
		sleep 0.1
		waited=$((waited + 1))
	done

	printf "  \033[31mFAIL\033[0m  ffmpeg outlived this suite by more than 15s:%s\n" "$left" >&2
	for pid in $left; do
		ps -o pid=,ppid=,command= -p "$pid" 2>/dev/null \
			| sed -E 's#(rtmps?://|srt://)[^ ]*#\1<redacted>#g' | cut -c1-120 | sed 's/^/            /' >&2
	done

	# REAPED, NOT MERELY NAMED. Reporting alone is what let thirteen of these reach
	# five and a half days on a shared host. The suite that created them is the last
	# thing that knows their pids, so it is the last chance anything has to clean up
	# without a reboot. SIGTERM first, so a process mid-write to a file this suite
	# may still assert on gets to finalise it.
	kill $left 2>/dev/null
	sleep 1
	for pid in $left; do
		if kill -0 "$pid" 2>/dev/null; then kill -9 "$pid" 2>/dev/null; fi
	done
	printf "            Reaped by this teardown, so the host is left clean -- but nothing\n" >&2
	printf "            in polyemesis did it, and outside systemd nothing would have.\n" >&2
	printf "            See #448.\n" >&2
	return 1
}

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
  # exits REPLACES the script's own exit status. Exiting from here would let a
  # teardown hiccup overwrite a RED run's specific product failure with a generic
  # teardown one, which is the mistake this repository keeps paying for. The
  # combining is done by poly_cleanup_exit below, which is given the suite's own
  # status and can only ever turn a 0 into a 1.
  if [ "$stopfail" -gt 0 ] || [ "$portfail" -gt 0 ]; then
    printf "  \033[31mFAIL\033[0m  teardown did not complete: %s server(s) survived kill -9, %s port(s) still held.\n" "$stopfail" "$portfail"
    printf "        Everything this run reported after the point of failure was\n"
    printf "        measured against a machine that is not in the state the\n"
    printf "        teardown claimed. Treat the results as unsound, not as flaky.\n"
    return 1
  fi
  return 0
}

# poly_cleanup_exit <suite-status> <ports> [work] [bindports] -- the teardown
# verdict, COMBINED with the suite's own, in the one place all twelve suites can
# share.
#
# THE PRECEDENCE IS THE WHOLE POINT, and it is asymmetric on purpose:
#
#   suite 0, teardown ok    -> 0    a clean run stays clean
#   suite 0, teardown FAILS -> 1    a green run whose teardown did not complete
#                                   measured the tail of itself against a machine
#                                   in an unknown state; that is not a pass
#   suite 3, teardown ok    -> 3    the suite's own status, unchanged
#   suite 3, teardown FAILS -> 3    STILL the suite's own status. A teardown
#                                   failure must never overwrite a specific
#                                   product failure with a generic one; a
#                                   teardown problem misread as a product problem
#                                   is what this class costs hours to.
#
# So teardown can only ever turn a 0 into a 1. It can never renumber a red run.
#
# THE CALLER'S HALF is uniform across every suite and has to stay that way:
#
#   cleanup() { ...suite sweeps...; poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"; }
#   trap 'poly_teardown_trap $? cleanup' EXIT
#
# See poly_teardown_trap for why the trap is one call and not a `;`-list.
poly_cleanup_exit() {
  local rc="$1"
  shift
  if ! poly_cleanup "$@"; then
    # ONLY a 0 is promoted. An unconditional `rc=1`, or an `else rc=0` on the
    # success side, both RENUMBER a red run -- which is the one outcome the table
    # above forbids. A tidy-up of this function reached for exactly that once;
    # case 9 of scripts/test-lib-cleanup.sh caught it, and this comment is here so
    # the next one does not have to be caught.
    # DELIBERATELY `[`. $rc is the caller's first argument and is not validated
    # here. `[` rejects a non-integer loudly; `[[` would evaluate it as an
    # arithmetic expression, where a non-numeric value reads as 0 and would be
    # PROMOTED to 1 -- renumbering a run on exactly the axis the table above
    # forbids renumbering.
    if [ "$rc" -eq 0 ]; then
      rc=1
    fi
  fi
  # AFTER poly_cleanup has had its turn, so what this names is what teardown
  # failed to reach rather than what it had not got to yet.
  #
  # IT USED TO BE `trap 'poly_report_orphans' RETURN`, which could not have failed
  # the run even if it had wanted to: a RETURN trap fires after the return value
  # is already fixed. Moving it inline is the whole of what turns a printed
  # finding into a result.
  #
  # Same promotion rule as above, for the same reason -- only a 0 is promoted,
  # because renumbering a red run is the one outcome the table above forbids.
  if ! poly_report_orphans; then
    if [ "$rc" -eq 0 ]; then
      rc=1
    fi
  fi
  return "$rc"
}

# poly_teardown_trap <suite-status> [cleanup-fn] -- the EXIT trap, entire.
#
# WHY THE TRAP IS ONE CALL AND NOT A LIST. The obvious spelling is
# `trap 'poly_rc=$?; poly_watchdog_disarm; cleanup "$poly_rc"; exit $?' EXIT`,
# and it is correct -- but only because `poly_rc=$?` comes FIRST. poly_watchdog_disarm
# ends in a `kill` and a `wait`, so it clobbers `$?`; a reader who tidies the
# disarm to the front turns every red suite green and nothing says so.
#
# Passing `$?` as an ARGUMENT makes that ordering structural instead of a
# convention: the shell expands it while assembling the call, before anything in
# this function runs, so there is no window in which it can be lost. It also
# stops shellcheck reporting the intermediate variable as referenced-but-unassigned
# in all twelve suites, which is a warning nobody would have kept reading.
poly_teardown_trap() {
  local rc="$1" fn="${2:-cleanup}"

  # The watchdog library is sourced by the suites, not by this one, so its
  # absence is a valid configuration rather than an error.
  if declare -f poly_watchdog_disarm >/dev/null 2>&1; then
    poly_watchdog_disarm
  fi

  "$fn" "$rc"
  exit $?
}
