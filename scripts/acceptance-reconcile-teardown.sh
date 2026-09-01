#!/usr/bin/env bash
#
# Reconcile teardowns kill no children.
#
# WHY THIS EXISTS, and it is a story about a gate that was green the whole time.
#
# acceptance-recording-stop.sh already asserts that no child is SIGKILLed. It is
# in the required CI matrix, its threshold is zero, and it passed every run while
# the production host accumulated 53 escalations over three weeks.
#
# It was not broken. It counts ONE POPULATION: a whole-server SIGTERM, with
# recording on and the input still flowing. Production's kills came from a
# different population entirely -- the reconcile paths, where an operator
# switches ingest mode, stops a source, or changes which source is on air. The
# feed stops BEFORE the child is signalled on those paths, and an FFmpeg blocked
# in a timeout-less read on a source that has gone quiet does not act on SIGTERM
# (internal/engine/manager.go). Every one of them waits out the grace period.
#
# So the populations diverged and nobody noticed, because a fixed-value check is
# only ever as good as the population it counts. This suite counts the other
# three.
#
# WHAT IT DOES DIFFERENTLY. The existing gate greps the whole log once, at the
# end. That answers "did anything get killed" and cannot answer "by what", which
# matters when three trajectories run in one process. This snapshots the count
# before each trajectory and asserts the delta, so a failure names the operation
# that caused it rather than the suite that noticed.
#
# Usage:  ./scripts/acceptance-reconcile-teardown.sh [workdir]
set -u

WORK="${1:-/tmp/polyemesis-acceptance-reconcile}"
PORT=8097
SRT_PORT=6097
RTMP_PORT=1937
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"
. "$SCRIPTS/lib-watchdog.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"
. "$SCRIPTS/lib-preflight.sh"

trap 'poly_verdict_trap $?' EXIT

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-reconcile-source" 2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK"
}
trap 'poly_teardown_trap $? cleanup' EXIT

poly_require_exec "$BIN"
poly_require_cmd curl "used to drive the server's API"
poly_require_cmd python3 "used to build the settings/source JSON documents"
command -v ffmpeg >/dev/null || poly_preflight_fail "ffmpeg is required"
ffmpeg -hide_banner -protocols 2>/dev/null | tr ' ' '\n' | grep -qx srt || \
  poly_preflight_fail "this suite needs an FFmpeg with libsrt"
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK" || exit 1
poly_watchdog_arm

PW='Reconcile-4v!Rt8#kQ'
API="http://127.0.0.1:$PORT/api/v1"
CJ="$WORK/cookies"
csrf() { awk '/polyemesis_csrf/{print $7}' "$CJ"; }

# THE ASSERTION, and the string it depends on.
#
# internal/supervisor/supervisor.go logs this verbatim when a child outlives its
# grace period. Nothing links that line to this grep at compile time, which is
# why internal/supervisor/grace_string_test.go pins both halves -- reword the log
# and that test fails rather than this suite silently passing by finding nothing.
KILL_LINE="did not exit after grace period"
kills_so_far() { grep -c "$KILL_LINE" server.log 2>/dev/null || echo 0; }

# assert_no_kills <before-count> <label>
#
# Attributing the kill to the trajectory is the point. Reporting "something was
# killed during this run" is what the existing gate already does.
assert_no_kills() {
  local before="$1" label="$2" after delta
  after=$(kills_so_far)
  delta=$(( after - before ))
  if [ "$delta" -eq 0 ]; then
    ok "$label: no child outlived its grace period"
  else
    bad "$label: $delta child(ren) SIGKILLed"
    grep "$KILL_LINE" server.log | tail -"$delta" | sed 's/^/        /'
  fi
}

step "1. Server and first-run setup"
"$BIN" --addr "127.0.0.1:$PORT" --data "$WORK/data" >server.log 2>&1 &
for _ in $(seq 1 50); do
  curl -fsS -m2 "$API/health" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS -m5 "$API/health" >/dev/null 2>&1 \
  && ok "server is up" || { bad "server did not start"; exit 1; }
# BOTH CALLS CARRY A USERNAME, AND THE ROUTE IS /auth/login. An earlier version
# omitted the username and posted to /login: handleSetup rejects a blank
# username outright, and /login is not a route at all. Copied from
# acceptance-recording-stop.sh rather than reconstructed, which is what should
# have happened the first time.
curl -fsS -m5 -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" "$API/setup" >/dev/null 2>&1 \
  && ok "first-run setup accepted" || { bad "setup failed"; tail -20 server.log; exit 1; }
curl -fsS -m5 -c "$CJ" -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" "$API/auth/login" >/dev/null 2>&1 \
  && ok "logged in" || { bad "login failed"; tail -20 server.log; exit 1; }

step "2. A source publishing over SRT"
curl -fsS -m5 -b "$CJ" "$API/settings" > settings.json 2>/dev/null || { bad "could not read settings"; exit 1; }
python3 - "$SRT_PORT" "$RTMP_PORT" <<'PY' || { echo "could not build settings"; exit 1; }
import json, sys
s = json.load(open("settings.json"))
s["listeners"]["srtPort"] = int(sys.argv[1])
s["listeners"]["rtmpPort"] = int(sys.argv[2])
json.dump(s, open("settings.new.json", "w"))
PY
curl -fsS -m10 -b "$CJ" -X PUT -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @settings.new.json "$API/settings" >/dev/null 2>&1 \
  && ok "listeners moved off the defaults" || { bad "could not write settings"; exit 1; }

python3 - <<'SRCDOC' || { echo "could not build the source document"; exit 1; }
import json
s = json.load(open("settings.new.json"))
ing = dict(s.get("ingest") or {})
ing["mode"] = "srt"
json.dump({"name": "Main", "enabled": True, "ingest": ing},
          open("source.srt.json", "w"))
SRCDOC
curl -fsS -m10 -b "$CJ" -X POST -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @source.srt.json "$API/sources" >/dev/null 2>&1 \
  && ok "created the source" || { bad "could not create the source"; exit 1; }

SRC_ID=$(curl -fsS -m5 -b "$CJ" "$API/sources" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])' 2>/dev/null)
TOKEN=$(curl -fsS -m5 -b "$CJ" "$API/sources" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["token"])' 2>/dev/null)
[ -n "$TOKEN" ] && ok "got the publish token" || { bad "no publish token"; exit 1; }

step "3. Publish until the ingest is measured"
# The wedge only happens once a child is actually reading, so a trajectory run
# against a never-started ingest would pass without testing anything.
# Lifted from acceptance-recording-stop.sh, which is the invocation known to
# work on the runners. Two differences from a first draft worth keeping: the
# process is identified by -metadata title rather than a drawtext filter, so it
# needs no libfreetype and cleanup can still pkill it by name; and -t bounds it,
# because -shortest across two INFINITE lavfi inputs is not short.
ffmpeg -hide_banner -loglevel error -re \
  -f lavfi -i "testsrc2=size=320x180:rate=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -map 0:v -map 1:a \
  -c:v libx264 -preset ultrafast -tune zerolatency -b:v 600k -pix_fmt yuv420p \
  -c:a aac -b:a 96k -metadata title=acceptance-reconcile-source \
  -t 180 -f mpegts \
  "srt://127.0.0.1:$SRT_PORT?streamid=$TOKEN&mode=caller&transtype=live&latency=200000" \
  >publisher.log 2>&1 &
PUB=$!
# WAIT ON "probed", NOT ON A GUESS. SourceInfo.Probed is set when the ingest
# layout has actually been measured, which is the state every child this suite
# tears down is waiting for. An earlier version polled for a "live" field that
# /status does not have, so the loop could only ever time out -- a readiness
# gate that cannot succeed fails the suite for the wrong reason and teaches
# nobody anything.
measured=0
for _ in $(seq 1 60); do
  if curl -fsS -m2 -b "$CJ" "$API/status" 2>/dev/null \
     | python3 -c 'import sys,json; print(json.load(sys.stdin).get("source",{}).get("probed") is True)' 2>/dev/null \
     | grep -qx True; then
    measured=1; break
  fi
  sleep 0.5
done
[ "$measured" = 1 ] && ok "the ingest is live and measured" \
  || { bad "the publisher never went live; nothing to tear down"; exit 1; }
sleep 2   # let meters/preview/loudness actually start and begin reading

# ---------------------------------------------------------------- trajectory 1
step "4. Trajectory A — ingest mode switch (SRT -> RTMP)"
# The operator-reported case. Switching mode reconciles the source, which stops
# every child reading the old relay.
B=$(kills_so_far)
python3 - <<'PY' || { echo "could not build the rtmp source document"; exit 1; }
import json
d = json.load(open("source.srt.json"))
d["ingest"]["mode"] = "rtmp"
json.dump(d, open("source.rtmp.json", "w"))
PY
T0=$(date +%s.%N)
curl -fsS -m20 -b "$CJ" -X PUT -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @source.rtmp.json "$API/sources/$SRC_ID" >/dev/null 2>&1 \
  && ok "switched ingest to RTMP" || bad "the mode switch was refused"
sleep 10   # longer than one full grace period, so a wedge has time to be logged
T1=$(date +%s.%N)
assert_no_kills "$B" "mode switch"
printf "        (switch took %ss)\n" "$(echo "$T1 - $T0" | bc)"

# ---------------------------------------------------------------- trajectory 2
step "5. Trajectory B — source stopped"
# The SRT publisher was dropped by trajectory A, so this and trajectory C tear
# down children whose feed has ALREADY gone quiet. That is not a weaker test --
# it is precisely the wedge condition internal/engine/manager.go describes, and
# the one a live-feed teardown cannot reach.
B=$(kills_so_far)
python3 - <<'PY' || { echo "could not build the disabled source document"; exit 1; }
import json
d = json.load(open("source.rtmp.json"))
d["enabled"] = False
json.dump(d, open("source.off.json", "w"))
PY
curl -fsS -m20 -b "$CJ" -X PUT -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @source.off.json "$API/sources/$SRC_ID" >/dev/null 2>&1 \
  && ok "source disabled" || bad "could not disable the source"
sleep 10
assert_no_kills "$B" "source stop"

# ---------------------------------------------------------------- trajectory 3
step "6. Trajectory C — source re-enabled and switched back"
B=$(kills_so_far)
curl -fsS -m20 -b "$CJ" -X PUT -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @source.srt.json "$API/sources/$SRC_ID" >/dev/null 2>&1 \
  && ok "source re-enabled on SRT" || bad "could not re-enable the source"
sleep 10
assert_no_kills "$B" "source change"

step "7. The whole run, counted once more"
# Belt and braces against a trajectory whose kill landed outside its own window:
# the per-trajectory deltas are the diagnosis, this total is the verdict.
TOTAL=$(kills_so_far)
if [ "$TOTAL" -eq 0 ]; then
  ok "no child was SIGKILLed at any point in this run"
else
  bad "$TOTAL escalation(s) across the whole run"
fi

kill "$PUB" 2>/dev/null
printf "\n%d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
