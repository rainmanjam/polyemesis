#!/usr/bin/env bash
# Tests for termination-guard.sh.
#
# WHY THIS EXISTS
#
# Eight tests have shipped in this repository that passed for the wrong reason,
# and sbom-guard.sh taught the lesson directly: a check nobody has watched FAIL
# is a check nobody has evidence works. The workflow guard this one is modelled
# on -- internal/testenv/workflowtimeout_test.go -- learned the same thing and
# keeps red fixtures for it.
#
# So: `red` holds shapes the guard MUST flag, `green` holds shapes it must NOT,
# and both the COUNT and the IDENTITY of the findings are asserted. Count alone
# would be satisfied by a guard that flagged the wrong four lines; identity alone
# would be satisfied by one that also flagged six more.
#
# Every red fixture is a real shape from this repository's history rather than
# one chosen to be easy to catch: #179's kill-then-sleep, acceptance-mqtt.sh's
# kill-then-verdict, acceptance.sh's kill-then-read, poly_cleanup's old bare
# `wait`, and #208's `$!` after a pipeline. Every green fixture is a site in the
# tree that gets it right.
#
# Usage:  ./scripts/test-termination-guard.sh
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
GUARD="$SCRIPTS/termination-guard.sh"

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

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/red" "$work/green" "$work/empty"

# ----------------------------------------------------------------- red fixtures
#
# LINE NUMBERS ARE PART OF THE ASSERTION, so these are written exactly as they
# are meant to be read. Adding a line to a fixture means updating the expectation
# below, which is the point: the guard has to name the offending LINE, not merely
# the file.

cat >"$work/red/kill-then-sleep.sh" <<'RED'
#!/usr/bin/env bash
# #179's body. A signal is sent and a fixed sleep stands in for the observation.
# Only the sleep, so that this fixture is what proves the sleep is in the
# vocabulary -- with a read underneath it, kill-then-read.sh would be catching
# this one too and neither token would be load-bearing.
stop() {
	pkill -f "widget --port 9000"
	sleep 2
}
RED

cat >"$work/red/kill-then-verdict.sh" <<'RED'
#!/usr/bin/env bash
# acceptance-mqtt.sh's old step 11: the verdict is printed as though the SIGKILL
# had already landed, so the check that follows measures our teardown.
pid=$(cat /tmp/widget.pid)
kill -9 "$pid"
ok "the widget was killed with no chance to say goodbye"
RED

cat >"$work/red/kill-then-read.sh" <<'RED'
#!/usr/bin/env bash
# acceptance.sh:134 before it was rewritten. ffprobe against a half-flushed
# Matroska reports missing audio bands: a routing verdict from a teardown that
# had not finished.
kill -TERM "$SINK_PID"
ffprobe -v error -show_streams rtmp-out.mkv
RED

cat >"$work/red/bare-wait.sh" <<'RED'
#!/usr/bin/env bash
# poly_cleanup ended on this until poly_wait_jobs replaced it: an unbounded wait
# in the shared teardown of all twelve suites.
cleanup() {
	pkill -f widget
	wait
}
RED

cat >"$work/red/pipeline-pid.sh" <<'RED'
#!/usr/bin/env bash
# #208 verbatim. After a pipeline `$!` is the LAST element, so W_PID names sed
# and the TERM below never reaches the widget.
widget --verbose 2>&1 | sed 's/^/  [w] /' &
W_PID=$!
kill -TERM "$W_PID"
RED

# --------------------------------------------------------------- green fixtures

cat >"$work/green/observed-kill.sh" <<'GREEN'
#!/usr/bin/env bash
# acceptance-postprod.sh:125. Ask, then watch, then say so.
pkill -9 -f "widget --port 9000"
for _ in $(seq 1 12); do
	pgrep -f "widget --port 9000" >/dev/null 2>&1 || break
	sleep 0.25
done
ok "the widget is gone"
GREEN

cat >"$work/green/one-line-stop.sh" <<'GREEN'
#!/usr/bin/env bash
# acceptance-tls.sh:76. The observation is on the kill's own line, which a guard
# that only ever looked at SUBSEQUENT lines would miss.
stop() { pkill -f "widget --port $1" 2>/dev/null; poly_free_port "$1"; }
sleep 1
GREEN

cat >"$work/green/subshell-pid.sh" <<'GREEN'
#!/usr/bin/env bash
# test-lib-watchdog.sh:140. `( ... | ... ) &` backgrounds the SUBSHELL, so `$!`
# is the subshell's pid -- which is exactly what the author meant. Flagging this
# is the false-positive rate that would make the rule grow an override list.
("$SUITE" | cat >out.log 2>&1) &
pipe_pid=$!
wait "$pipe_pid"
GREEN

cat >"$work/green/kill-zero-is-observation.sh" <<'GREEN'
#!/usr/bin/env bash
# `kill -0` sends no signal; it asks whether the process is still there. Reading
# it as a kill would flag every correct poll in the tree.
for _ in $(seq 1 40); do
	kill -0 "$PID" 2>/dev/null || break
	sleep 0.25
done
ok "gone"
GREEN

cat >"$work/green/verdict-before-cleanup.sh" <<'GREEN'
#!/usr/bin/env bash
# The verdict is emitted BEFORE the teardown. #179 cost 28 minutes and a
# BlobNotFound log because the answer was computed and then lost behind a
# cleanup that hung; this is the ordering that fixes it.
if pgrep -f widget >/dev/null; then
	bad "the widget outlived its deadline"
fi
pkill -9 -f widget
GREEN

# --------------------------------------------------------------------- the runs

# An empty allowlist by default, so the fixtures are judged on their own.
: >"$work/empty-allow"
run_guard() { # run_guard <dir> [allowlist-file]
	TERMINATION_GUARD_ALLOWLIST="${2:-$work/empty-allow}" \
		"$GUARD" "$1" >"$work/out" 2>&1
}

findings() { grep -E '^(red|green|empty)/' "$work/out" | sed 's/^\([^:]*:[0-9]*: [a-z-]*\):.*/\1/' | sort; }

step "1. Every red fixture is flagged, by count and by identity"
# THE CENTRAL CASE. `want` names file, line and rule; a guard that flagged the
# right number of the wrong lines fails here, and so does one that flagged these
# five and three more besides.
want="red/bare-wait.sh:6: bare-wait
red/kill-then-read.sh:5: kill-then-assume
red/kill-then-sleep.sh:7: kill-then-assume
red/kill-then-verdict.sh:5: kill-then-assume
red/pipeline-pid.sh:4: pipeline-pid"
run_guard "$work/red"
rc=$?
got="$(findings)"
if [ "$rc" -eq 0 ]; then
	bad "the guard passed a directory of five known defects"
	cat "$work/out"
else
	ok "the guard rejects the red fixtures"
fi
nwant=$(printf '%s\n' "$want" | wc -l | tr -d ' ')
ngot=$(printf '%s\n' "$got" | grep -c . || true)
if [ "$ngot" -ne "$nwant" ]; then
	bad "flagged $ngot lines, want $nwant"
	printf '  got:\n%s\n  want:\n%s\n' "$got" "$want"
else
	ok "flagged $ngot lines, one per red fixture"
fi
if [ "$got" = "$want" ]; then
	ok "each finding names the right file, line and rule"
else
	bad "the findings are not the expected ones"
	diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | sed 's/^/        /'
fi

step "2. No green fixture is flagged"
# The other half. A rule that flagged everything would satisfy case 1 entirely.
if run_guard "$work/green"; then
	ok "the guard accepts five sites that observe the death"
else
	bad "the guard flagged code that re-observes; this is the false-positive rate that makes a gate grow an override list"
	cat "$work/out"
fi

step "3. A directory it cannot see into is FATAL, not clean"
# The failure mode this whole round is about: a renamed directory reading as a
# clean sweep. Distinct exit status, so a CI log can tell "nothing wrong" from
# "nothing looked at".
run_guard "$work/empty"
rc=$?
if [ "$rc" -eq 2 ]; then
	ok "a directory with no *.sh in it exits 2 rather than 0"
else
	bad "scanning nothing exited $rc; a renamed scripts/ would read as a clean sweep"
fi
if grep -q "examining nothing" "$work/out"; then
	ok "and says why, rather than printing an empty report"
else
	bad "the fatal is silent about what happened"
fi

step "4. An allowlist entry suppresses exactly what it names"
printf 'red/kill-then-sleep.sh\tkill-then-assume\tfixture: the mechanism has to be exercised by something\n' >"$work/allow-one"
run_guard "$work/red" "$work/allow-one"
got="$(findings)"
if printf '%s' "$got" | grep -q "kill-then-sleep"; then
	bad "an allowlisted finding was still reported"
elif [ "$(printf '%s\n' "$got" | grep -c .)" -eq 4 ]; then
	ok "one entry suppressed one finding and left the other four"
else
	bad "the allowlist suppressed more than it named: $got"
fi

step "5. An exception with no reason is itself an error"
# The entry is otherwise valid and would suppress a real finding. What makes it
# fail is the empty third field: an exception nobody wrote a reason for is one
# nobody argued for, and this repository has invented six free passes already.
printf 'red/kill-then-sleep.sh\tkill-then-assume\t\n' >"$work/allow-noreason"
run_guard "$work/red" "$work/allow-noreason"
if grep -q "has no reason" "$work/out"; then
	ok "an allowlist entry with an empty reason is rejected by name"
else
	bad "an entry with no reason was accepted silently"
	cat "$work/out"
fi

step "6. An exception that suppresses nothing is an error too"
# A stale entry is a standing licence nobody re-argued for, and the file it names
# may since have grown a real instance that it would silently cover.
printf 'green/observed-kill.sh\t*\tfixture: nothing here to suppress\n' >"$work/allow-stale"
run_guard "$work/green" "$work/allow-stale"
rc=$?
if [ "$rc" -ne 0 ] && grep -q "suppress nothing" "$work/out"; then
	ok "a stale entry fails the guard rather than sitting there"
else
	bad "a stale allowlist entry passed (rc=$rc); an exception can outlive its reason unnoticed"
	cat "$work/out"
fi

step "7. The real tree passes its own guard"
# Cases 1-6 are about the guard. This is about the tree: the guard is only worth
# running if the thing it guards is actually clean, and a guard that ships red is
# one somebody turns off.
if "$GUARD" >"$work/real" 2>&1; then
	ok "$(grep '^TERMINATION GUARD: ok' "$work/real")"
else
	bad "the guard fails against scripts/ -- fix the finding or allowlist it with a reason"
	sed 's/^/        /' "$work/real"
fi

total=$((pass + fail))
EXPECTED_CHECKS=10
printf "\n"
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
	printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
	exit 1
fi
if [ "$fail" -gt 0 ]; then
	printf "  \033[31mtermination-guard: %d of %d checks FAILED\033[0m\n\n" "$fail" "$total"
	exit 1
fi
printf "  \033[32mtermination-guard: %d checks passed\033[0m\n\n" "$total"
