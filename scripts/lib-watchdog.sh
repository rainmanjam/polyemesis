#!/usr/bin/env bash
# A deadline the acceptance suites own, so a hang reports instead of vanishing.
#
# WHY THIS EXISTS
#
# ci.yml already states the principle, about the Go tests:
#
#   Kept BELOW timeout-minutes on purpose. Go's timeout panics with a goroutine
#   dump naming the test that was running; the job timeout just kills the runner
#   and tells you nothing. Whichever fires first decides how much you learn, so
#   Go's has to.
#
# The acceptance suites had no inner deadline at all. The job's twenty-minute
# ceiling was the only one, so when acceptance-synth hung on PR #75 GitHub
# cancelled it and the log ended mid-sentence. Issue #38's title is "reruns
# erase the evidence"; a cancel erases it without anyone even rerunning.
#
# Four suites have now shown this shape -- mqtt, failover, renditions, synth --
# and they have nothing in common except that each publishes a real stream and
# waits for it to arrive. So the deadline belongs to all of them, not to the
# ones that have failed so far.
#
# WHAT IT DOES NOT DO
#
# It does not shorten, lengthen or retry anything. A suite that would have hung
# for twenty minutes still hangs; it is killed five minutes earlier and says
# what it was doing. The verdict is unchanged and always a failure -- a watchdog
# that could pass a suite would be a watchdog nobody could trust, for the same
# reason lib-observe.sh refuses to let its post-ceiling sample change a verdict.
#
# USAGE
#
#   . "$(dirname "$0")/lib-watchdog.sh"
#   poly_watchdog_arm
#   step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }
#   cleanup() { poly_watchdog_disarm; poly_cleanup "$PORT" "$WORK"; }

# Seconds before the watchdog fires. 900 against ci.yml's timeout-minutes: 20,
# leaving five minutes for the report itself and for the suite's teardown trap
# to run -- teardown kills FFmpeg children and can take a dozen seconds per
# child, and a report that is itself cancelled would be no better than none.
POLY_WATCHDOG_SECS="${POLY_WATCHDOG_SECS:-900}"

# How often the watchdog wakes. It checks two things -- the deadline, and
# whether the suite is still alive -- so this also bounds how long an orphaned
# watchdog can outlive a suite that died without disarming.
POLY_WATCHDOG_TICK="${POLY_WATCHDOG_TICK:-2}"

# How many lines of the server log the report carries. Enough to show what the
# engine was doing, short enough that the step name above it is still visible.
POLY_WATCHDOG_LOG_LINES="${POLY_WATCHDOG_LOG_LINES:-25}"

# The breadcrumb has to be a FILE. The watchdog is a separate process and
# cannot read the suite's shell variables, which is the whole reason the last
# step reached was unrecoverable before this existed.
POLY_STEP_FILE="${POLY_STEP_FILE:-}"
poly__watchdog_pid=""

# poly_step_record <name> -- remember the step the suite has just entered.
#
# Records the entry TIME as well as the name. "stuck in step 4" and "stuck in
# step 4 for eleven of the fifteen minutes" are different reports, and only the
# second one tells you whether the step is slow or wedged.
poly_step_record() {
  [ -n "$POLY_STEP_FILE" ] || return 0
  printf '%s %s\n' "$(date +%s)" "$1" > "$POLY_STEP_FILE"
}

# poly__watchdog_match_procs -- reads `ps -eo pid,etime,comm,args` on stdin and
# prints the lines that are ours.
#
# MATCHED ON THE FIRST ARGV TOKEN, not on COMM and not on the whole line, and
# both halves of that were learned the hard way.
#
# Not COMM: macOS truncates that column to sixteen characters, so
# /opt/homebrew/bin/ffmpeg arrives as "/opt/homebrew/bi" and an anchored match
# on it finds nothing. Linux does not truncate, so a COMM match works there --
# meaning the first version of this passed CI while reporting "nothing of ours
# was still running" on a laptop with four FFmpegs live in front of it. A
# diagnostic that is confidently wrong is worse than none.
#
# Not the whole line either: a suite lives under a directory called polyemesis,
# so a substring match reports the suite's own shell as a live process.
#
# Lines are truncated because an FFmpeg invocation is three hundred columns of
# filter graph, and the point of this block is the four names above it.
poly__watchdog_match_procs() {
  awk '{ n = split($4, seg, "/")
         if (seg[n] ~ /^(polyemesis|ffmpeg|mosquitto)$/) print substr($0, 1, 160) }'
}

# poly__watchdog_report <elapsed-secs> -- everything known at the moment the
# deadline passed. Printed by the watchdog process, before it kills anything,
# because a report that races the kill is a report that sometimes does not
# arrive.
poly__watchdog_report() {
  local elapsed="$1" entry name since
  printf '\n=== WATCHDOG: %ss elapsed, the suite has not finished ===\n' "$elapsed"

  if [ -s "$POLY_STEP_FILE" ]; then
    entry="$(cut -d' ' -f1 < "$POLY_STEP_FILE")"
    name="$(cut -d' ' -f2- < "$POLY_STEP_FILE")"
    since=$(( $(date +%s) - entry ))
    printf '  last step reached: %s\n' "$name"
    printf '                     entered %ss ago, at t+%ss\n' "$since" "$((elapsed - since))"
  else
    # Not the same as "no step": a suite can hang in its setup, before the
    # first step() call, and saying "(none)" would read as a broken breadcrumb.
    printf '  last step reached: (none -- it hung before the first step)\n'
  fi

  # The suite's own children. `ps` rather than the process tree because macOS
  # and Linux disagree on the flags for a tree, and the names are what identify
  # the stage: a live ffmpeg means a publisher that never ended, a live
  # polyemesis with no ffmpeg means an engine waiting for one.
  printf '  live processes:\n'
  local procs
  # shellcheck disable=SC2009  # pgrep cannot print etime and args together portably
  procs="$(ps -eo pid,etime,comm,args 2>/dev/null | poly__watchdog_match_procs | head -12)"
  if [ -n "$procs" ]; then
    printf '%s\n' "$procs" | sed 's/^/    /'
  else
    printf '    (none -- nothing of ours was still running)\n'
  fi

  # server.log is the conventional name across every suite. Absent is a real
  # answer -- it means the hang happened before the server was started.
  #
  # Truncated per line for the same reason the process list is: the engine logs
  # every FFmpeg it starts at DEBUG, argv and all, so twenty-five untruncated
  # lines is several thousand columns and pushes the step name off the reader's
  # screen. Measured on a real firing before this was added.
  printf '  server.log tail:\n'
  if [ -f server.log ]; then
    tail -n "$POLY_WATCHDOG_LOG_LINES" server.log | cut -c1-200 | sed 's/^/    /'
  else
    printf '    (no server.log in %s -- the server was never started here)\n' "$PWD"
  fi

  printf '  === killing the suite so this output survives the job ceiling ===\n\n'
}

# poly_watchdog_arm [secs] -- start the deadline. Safe to call once; a second
# call replaces the first rather than running two.
poly_watchdog_arm() {
  local secs="${1:-$POLY_WATCHDOG_SECS}" target=$$

  poly_watchdog_disarm
  [ -n "$POLY_STEP_FILE" ] || POLY_STEP_FILE="$(mktemp -t poly-step.XXXXXX)"
  : > "$POLY_STEP_FILE"

  (
    local start elapsed
    start=$(date +%s)
    while :; do
      sleep "$POLY_WATCHDOG_TICK"
      # The suite finished or died without disarming. Exit quietly: a watchdog
      # that announced every clean run would train people to skim past it.
      kill -0 "$target" 2>/dev/null || exit 0
      elapsed=$(( $(date +%s) - start ))
      if [ "$elapsed" -ge "$secs" ]; then
        poly__watchdog_report "$elapsed"
        # TERM, not KILL: the suite's EXIT trap is what stops the engine and
        # reaps its FFmpeg children, and skipping it would leave the runner
        # holding ports that the next suite in the matrix needs.
        kill -TERM "$target" 2>/dev/null
        exit 0
      fi
    done
  ) &
  poly__watchdog_pid=$!
}

# poly_watchdog_disarm -- stop the deadline. Call from the suite's EXIT trap.
#
# WHAT THIS IS AND IS NOT FOR. A first draft of this comment claimed disarming
# was mandatory, on the grounds that a surviving background child holds the
# step's stdout open and a CI step does not end until that pipe closes. A
# mutation that made this function a no-op did not turn a single check red,
# which is how the claim was found to be false: the watchdog checks that its
# suite is still alive every POLY_WATCHDOG_TICK and exits on its own when it
# is not. Undisarmed, it costs a tick -- two seconds -- not a hang.
#
# So this exists for the two things it does buy: the tick is not spent, and the
# breadcrumb file is removed rather than left in the temp directory of every CI
# runner and every maintainer's laptop. The second is what the guard watches,
# because it is the one an assertion can see without timing anything.
poly_watchdog_disarm() {
  [ -n "$poly__watchdog_pid" ] || return 0
  kill "$poly__watchdog_pid" 2>/dev/null
  wait "$poly__watchdog_pid" 2>/dev/null
  poly__watchdog_pid=""
  [ -n "$POLY_STEP_FILE" ] && rm -f "$POLY_STEP_FILE"
  return 0
}
