#!/usr/bin/env bash
# Shared observation helpers for the host acceptance suites.
#
# WHY THIS EXISTS
#
# Issue #38 records two suites failing and passing on rerun with no code change.
# Neither failure left enough behind to tell what had actually gone wrong:
#
#   acceptance-failover  "the primary never went on air: slate 1 false 0"
#   acceptance-mqtt      "the broker container did not start"
#
# Both messages assert a cause the check never measured. The first reports a
# SINGLE status line sampled fresh AFTER the deadline, so a primary that arrived
# 200 ms late is indistinguishable from one that never arrived at all. The
# second is a guess: the condition tested is `docker ps`, which cannot separate
# "never created" from "exited immediately" from "still pulling" -- and the
# `docker run` that would have said so had its stdout, its stderr and its exit
# status all discarded at the call site.
#
# A wait that gives up must report what it SAW, not what it concluded. That
# distinction is the whole point of this file: every helper here records
# observations and prints them on failure, and none of them changes a timeout,
# adds a retry, or decides a verdict. Those are separate decisions that need the
# flake rate measured first -- see .github/workflows/flake-rate.yml, which is
# report-only for the same reason.
#
# This shape is repo-wide: fourteen suites carry twenty-odd `for _ in $(seq ...)`
# poll loops that break on success and say nothing on failure. Only the two
# suites named in issue #38 have been converted so far, deliberately -- the
# helper should prove itself against the failures that actually happen before
# twelve more suites are rewritten to use it.
#
# USAGE
#
#   . "$(dirname "$0")/lib-observe.sh"
#
#   sample_active() { readstatus | awk '{print $1}'; }
#   poly_poll_until "the primary goes on air" primary 40 sample_active \
#     || { bad "..."; exit 1; }
#
# On success it is silent and sets POLY_WAIT_ELAPSED. On failure it prints the
# run-length-encoded trajectory of everything it observed, then returns 1.

# How often a poll takes a sample, in seconds. 0.5 matches the cadence the
# failover suite already used; it is named here so the report can convert a
# sample index back into wall-clock time.
POLY_POLL_INTERVAL="${POLY_POLL_INTERVAL:-0.5}"

# A CEILING IS A CEILING, NOT A FLOOR. The deadline below is built from
# `date +%s`, which counts whole seconds, so a ceiling of N seconds buys
# anywhere between N-1 and N seconds of actual polling -- decided by nothing but
# where in the wall-clock second the call happened to land. A ceiling of 1 can
# therefore take a SINGLE sample and give up.
#
# That is fine for the acceptance suites, whose ceilings are tens of seconds and
# whose samplers are asking a real server a real question. It is a trap for
# anything asserting on how many samples a wait produced: test-lib-observe.sh
# step 6 did exactly that at ceiling 1 and failed about one run in twenty until
# its ceiling was raised. Sub-second precision here would mean date +%s%N or
# per-platform date flags, which is not worth it -- so the granularity is
# documented instead, and callers who care about a sample FLOOR should raise the
# ceiling rather than assume one.

# Cap on how many distinct runs the trajectory prints. A stable signal produces
# one or two runs, so this only bites on a value that oscillates -- exactly the
# case where an unbounded dump would bury the reader in noise.
POLY_TRAJ_MAX_RUNS="${POLY_TRAJ_MAX_RUNS:-12}"

# Set by poly_poll_until so a caller can report how long it actually waited
# rather than how long it was willing to wait. Those differ, and the difference
# is what tells you a ceiling is too low.
POLY_WAIT_ELAPSED=0

# The trajectory, run-length encoded as "<first-sample-index>|<count>|<value>"
# entries. Consecutive identical samples collapse into one run, which is what
# makes "never moved" (one run), "moved late" (two runs, the second starting
# near the deadline) and "oscillated" (many runs) distinguishable at a glance.
poly__traj=()

poly__traj_reset() { poly__traj=(); }

# poly__traj_add <sample-index> <value>
poly__traj_add() {
  local idx="$1" val="$2" n last lastval lastcount lastidx
  n=${#poly__traj[@]}
  if [ "$n" -gt 0 ]; then
    last="${poly__traj[$((n - 1))]}"
    lastidx="${last%%|*}"
    lastcount="${last#*|}"; lastcount="${lastcount%%|*}"
    lastval="${last#*|*|}"
    if [ "$lastval" = "$val" ]; then
      poly__traj[$((n - 1))]="$lastidx|$((lastcount + 1))|$lastval"
      return
    fi
  fi
  poly__traj+=("$idx|1|$val")
}

# poly__traj_print — one line per run, oldest first, with the wall-clock offset
# at which each run began. The offset is derived from the sample index and the
# poll interval rather than measured per sample, because `date` on macOS has no
# sub-second format and the interval is the finer of the two resolutions anyway.
# poly__grace_check <wanted> <sampler-fn> [args...]
#
# One more sample AFTER the ceiling has expired.
#
# This is the single line that answers issue #38's actual question. A poll that
# stops at its deadline CANNOT observe a value that arrives just after it, so
# the trajectory alone still cannot separate "never happened" from "happened
# 200 ms too late" -- both end with the same last run. Taking one further
# sample once the verdict is already decided costs one poll interval and turns
# "the primary never went on air" into either a real product failure or a
# ceiling that is set too low, which are opposite conclusions.
#
# The verdict is NOT changed by what this finds. A suite that passed because the
# post-mortem was generous would be a suite with a timeout nobody can trust.
poly__grace_check() {
  local want="$1" fn="$2"; shift 2
  local after
  sleep "$POLY_POLL_INTERVAL"
  after="$("$fn" "$@" 2>&1)"
  if [ "$after" = "$want" ]; then
    printf '        NOTE: it became %s one sample AFTER the ceiling.\n' "$want"
    printf '              The ceiling is too low; this is not a product failure.\n'
  else
    printf '        one sample past the ceiling it was still: %s\n' "$after"
  fi
}

poly__traj_print() {
  local n runs shown i entry idx count val at
  n=${#poly__traj[@]}
  [ "$n" -eq 0 ] && { printf '        (no samples were taken)\n'; return; }
  runs="$n"
  shown="$n"
  if [ "$n" -gt "$POLY_TRAJ_MAX_RUNS" ]; then shown="$POLY_TRAJ_MAX_RUNS"; fi
  printf '        observed %d distinct run(s) across the wait:\n' "$runs"
  i=0
  while [ "$i" -lt "$shown" ]; do
    entry="${poly__traj[$i]}"
    idx="${entry%%|*}"
    count="${entry#*|}"; count="${count%%|*}"
    val="${entry#*|*|}"
    at=$(awk -v s="$idx" -v iv="$POLY_POLL_INTERVAL" 'BEGIN{printf "%.1f", (s-1)*iv}')
    printf '          t+%-6ss  x%-4d  %s\n' "$at" "$count" "$val"
    i=$((i + 1))
  done
  if [ "$shown" -lt "$runs" ]; then
    printf '          ... %d further run(s) elided\n' "$((runs - shown))"
  fi
}

# poly_poll_until <label> <wanted> <timeout-secs> <sampler-fn> [args...]
#
# Polls <sampler-fn> until it prints exactly <wanted>, or <timeout-secs>
# elapses. Returns 0 on match and 1 on timeout, printing the trajectory in the
# latter case.
#
# The sampler is a FUNCTION NAME, not a command string, so nothing here has to
# eval a caller's text.
poly_poll_until() {
  local label="$1" want="$2" secs="$3" fn="$4"; shift 4
  local start cur samples=0 deadline
  poly__traj_reset
  start=$(date +%s)
  deadline=$((start + secs))
  while :; do
    cur="$("$fn" "$@" 2>&1)"
    samples=$((samples + 1))
    poly__traj_add "$samples" "$cur"
    if [ "$cur" = "$want" ]; then
      POLY_WAIT_ELAPSED=$(( $(date +%s) - start ))
      return 0
    fi
    [ "$(date +%s)" -ge "$deadline" ] && break
    sleep "$POLY_POLL_INTERVAL"
  done
  POLY_WAIT_ELAPSED=$(( $(date +%s) - start ))
  printf '        waited %ss for %s (ceiling %ss, %d samples), wanted %s\n' \
    "$POLY_WAIT_ELAPSED" "$label" "$secs" "$samples" "$want"
  poly__traj_print
  poly__grace_check "$want" "$fn" "$@"
  return 1
}

# poly_poll_field <label> <field-idx> <wanted> <timeout-secs> <line-sampler-fn>
#
# The same poll, for a sampler that prints a whole whitespace-separated LINE of
# which only one field decides the match.
#
# Worth having as its own entry point rather than pushing an awk into the
# caller's sampler: the trajectory then records the ENTIRE line, so the report
# shows the fields nobody was waiting on. That is what turns issue #38's
# "slate 1 false 0" from a terse verdict into a readable one -- the switch count
# of 1 says a switch had already happened before the wait even began, which is
# the actual lead and is in a field the wait was not watching.
poly_poll_field() {
  local label="$1" idx="$2" want="$3" secs="$4" fn="$5"; shift 5
  local start line cur samples=0 deadline
  poly__traj_reset
  start=$(date +%s)
  deadline=$((start + secs))
  while :; do
    line="$("$fn" "$@" 2>&1)"
    cur="$(printf '%s' "$line" | awk -v n="$idx" '{print $n}')"
    samples=$((samples + 1))
    poly__traj_add "$samples" "$line"
    if [ "$cur" = "$want" ]; then
      POLY_WAIT_ELAPSED=$(( $(date +%s) - start ))
      return 0
    fi
    [ "$(date +%s)" -ge "$deadline" ] && break
    sleep "$POLY_POLL_INTERVAL"
  done
  POLY_WAIT_ELAPSED=$(( $(date +%s) - start ))
  printf '        waited %ss for %s (ceiling %ss, %d samples); field %d never became %s\n' \
    "$POLY_WAIT_ELAPSED" "$label" "$secs" "$samples" "$idx" "$want"
  poly__traj_print
  # The same post-ceiling sample as poly_poll_until, compared on the field
  # rather than the whole line, and printing the whole line either way.
  sleep "$POLY_POLL_INTERVAL"
  line="$("$fn" "$@" 2>&1)"
  cur="$(printf '%s' "$line" | awk -v n="$idx" '{print $n}')"
  if [ "$cur" = "$want" ]; then
    printf '        NOTE: field %d became %s one sample AFTER the ceiling.\n' "$idx" "$want"
    printf '              The ceiling is too low; this is not a product failure.\n'
  fi
  printf '        one sample past the ceiling the line was: %s\n' "$line"
  return 1
}

# Set by poly_hold_field: the value the field settled on, and how many times it
# was seen to change. Both are for the caller's message -- "held at 6" and
# "moved 31 times" are the two sentences a reader needs and neither is
# recoverable from the return code.
POLY_HELD_VALUE=""
POLY_HELD_CHANGES=0

# poly_hold_field <label> <field-idx> <hold-secs> <ceiling-secs> <line-sampler-fn> [args...]
#
# THE CEILING HALF OF AN ASSERTION WHOSE FLOOR IS ALREADY CHECKED.
#
# poly_poll_field waits for a field to REACH a value. This waits for a field to
# STOP MOVING: it returns 0 once one value has held for <hold-secs> within
# <ceiling-secs>, and 1 otherwise, printing the trajectory of everything it saw.
#
# Issue #226 is why it exists. `acceptance-failover.sh` asserted its switch count
# with `-ge 2` and nothing else, and a wrong-hub mutation that made the tier
# switch 80 times where the baseline switches 6 PASSED that check -- 80 is not
# fewer than 2. Failover's failure mode is not "too few switches"; it is
# flapping, and flapping sails past any floor.
#
# A CONSTANT CEILING IS THE WRONG FIX and was rejected before this was written.
# The legitimate switch count in that suite is timing-dependent -- the script's
# own measurements show switches landing at roughly 13s and 36s depending on how
# the sweep and the grace period line up -- so any number picked for `-le` is
# either so loose it admits flapping or so tight it fails correct code. What is
# NOT timing-dependent is that a tier nobody is asking to switch stops switching.
# That is a stability predicate, it needs no magic number, and it is what this
# measures.
#
# The field is compared, the whole LINE is recorded, for the same reason
# poly_poll_field does it: the report then shows the fields nobody was watching.
poly_hold_field() {
  local label="$1" idx="$2" hold="$3" secs="$4" fn="$5"; shift 5
  local start now line cur prev="" held_since=0 samples=0 deadline
  poly__traj_reset
  POLY_HELD_VALUE=""
  POLY_HELD_CHANGES=0
  start=$(date +%s)
  deadline=$((start + secs))
  while :; do
    line="$("$fn" "$@" 2>&1)"
    cur="$(printf '%s' "$line" | awk -v n="$idx" '{print $n}')"
    samples=$((samples + 1))
    poly__traj_add "$samples" "$line"
    now=$(date +%s)
    if [ "$samples" -eq 1 ] || [ "$cur" != "$prev" ]; then
      [ "$samples" -gt 1 ] && POLY_HELD_CHANGES=$((POLY_HELD_CHANGES + 1))
      prev="$cur"
      held_since=$now
    elif [ $((now - held_since)) -ge "$hold" ]; then
      POLY_HELD_VALUE="$cur"
      POLY_WAIT_ELAPSED=$((now - start))
      return 0
    fi
    [ "$now" -ge "$deadline" ] && break
    sleep "$POLY_POLL_INTERVAL"
  done
  POLY_HELD_VALUE="$prev"
  POLY_WAIT_ELAPSED=$(( $(date +%s) - start ))
  printf '        watched %s for %ss (ceiling %ss, %d samples); field %d never held one value for %ss\n' \
    "$label" "$POLY_WAIT_ELAPSED" "$secs" "$samples" "$idx" "$hold"
  printf '        it changed %d time(s) and was last %s\n' "$POLY_HELD_CHANGES" "${prev:-(empty)}"
  poly__traj_print
  return 1
}

# poly_docker_postmortem <container>
#
# What a container check should print instead of asserting a cause. Every line
# here answers a question `docker ps` cannot: whether the container exists at
# all, whether it exited and with what code, and what it said on the way out.
#
# Safe to call when the container was never created -- each command is allowed
# to fail and says so rather than emitting nothing.
poly_docker_postmortem() {
  local c="$1" state
  printf '        docker post-mortem for %s:\n' "$c"

  state=$(docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} err={{.State.Error}}' "$c" 2>&1) \
    || state="(no such container -- it was never created)"
  printf '          state: %s\n' "$state"

  # -a because a container that exited is invisible to plain `docker ps`, and
  # "exited 2 seconds ago" is the single most useful line in that case. An empty
  # result is reported as such: a blank value after a label reads as a tool that
  # failed to run, which is a different and misleading conclusion.
  local ps_line
  ps_line="$(docker ps -a --filter "name=^${c}$" --format '{{.Status}} ({{.Image}})' 2>&1 | head -1)"
  printf '          ps -a: %s\n' "${ps_line:-(no container by that name, running or exited)}"

  printf '          last log lines:\n'
  if ! docker logs --tail 20 "$c" 2>&1 | sed 's/^/            /'; then
    printf '            (no logs -- container absent or never started)\n'
  fi

  # A port already held by a previous run is the likeliest cause of a container
  # that starts and dies immediately, and it is invisible in the container's own
  # logs because Docker refuses the bind before mosquitto ever runs.
  printf '          port holders:\n'
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR==1 || /:1883|:'"${BROKER_PORT:-1883}"'/' \
      | head -6 | sed 's/^/            /'
  else
    printf '            (lsof unavailable)\n'
  fi
}
