#!/usr/bin/env bash
#
# Offline tests of scripts/demo-seed.sh --scene sports: its refusals, and the
# things it must not do before it has refused.
#
# A stub `docker` goes first on PATH. It answers `docker info` per
# STUB_DOCKER_UP, prints nothing for `docker ps`, records every call in
# $STUB_LOG and refuses to start the sports server. So the script can be driven
# to the point where it would create something, and each case can assert that it
# stopped before (no `network create` in the log) rather than only that the
# exit code was non-zero. No Docker, no network and no UI build are needed;
# `go run` of the seed driver is the only heavy step (its modules are already in
# the build cache for the Go tests).
#
# Prints `ok <n> <name>` per case and exits 1 on the first failure.

# shellcheck disable=SC1090 # the library is sourced by a variable path
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/demo-seed.sh"
LIB="$ROOT/scripts/lib-demo-sports.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/demo-seed-test.XXXXXX")"
LISTENER_PID=""

cleanup() {
  [ -n "$LISTENER_PID" ] && kill "$LISTENER_PID" 2>/dev/null
  command rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP/bin"
cat > "$TMP/bin/docker" <<'STUB'
#!/bin/sh
echo "$*" >> "$STUB_LOG"
case "$1" in
  info) [ "${STUB_DOCKER_UP:-1}" = 1 ]; exit $? ;;
  version) echo "${STUB_DOCKER_VERSION:-27.0.1}"; exit 0 ;;
  ps) exit 0 ;;
  network) [ "$2" = inspect ] && echo "${STUB_NET_INTERNAL:-true}"; exit 0 ;;
  port) [ -n "${STUB_PORT_LINE:-}" ] && echo "$STUB_PORT_LINE"; exit 0 ;;
esac
case "$*" in
  *run*polyemesis-demo-sports-server*) [ "${STUB_SERVER_OK:-0}" = 1 ] || exit 1 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/docker"
cat > "$TMP/bin/git" <<'STUB'
#!/bin/sh
exit 1
STUB
chmod +x "$TMP/bin/git"

N=0
fail() {
  echo "FAIL $N $NAME: $*" >&2
  exit 1
}
pass() { echo "ok $N $NAME"; }
begin() {
  N=$((N + 1)); NAME="$1"
  export DEMO_WORK="$TMP/work$N" STUB_LOG="$TMP/log$N" STUB_DOCKER_UP=1
  export DEMO_PORT; DEMO_PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')"
  OUT="$TMP/out$N"; ERR="$TMP/err$N"
  : > "$STUB_LOG"
}
# run_script <args...>: stub docker first on PATH, a version set so the capture
# guard is quiet, the image build skipped. Sets RC.
run_script() {
  PATH="$TMP/bin:$PATH" DEMO_VERSION="${DEMO_VERSION-v0.0.0-test}" DEMO_REUSE_IMAGE=1 \
    bash "$SCRIPT" "$@" > "$OUT" 2> "$ERR" < /dev/null
  RC=$?
}
expect_rc() { [ "$RC" = "$1" ] || fail "exit $RC, want $1 (stderr: $(tail -n 3 "$ERR" | tr '\n' ' '))"; }
expect_err() { grep -q -- "$1" "$ERR" || fail "stderr lacks \"$1\" (stderr: $(tail -n 3 "$ERR" | tr '\n' ' '))"; }
expect_no_call() { ! grep -q -- "$1" "$STUB_LOG" || fail "the stub log has \"$1\": $(grep -- "$1" "$STUB_LOG" | head -n 1)"; }

# Portable (BSD and GNU stat differ): Python reads the mode.
mode_of() { python3 -c 'import os,sys;print(oct(os.stat(sys.argv[1]).st_mode & 0o777)[2:])' "$1"; }
open_paths() {
  python3 - "$1" <<'PY'
import os, sys
for d, dirs, files in os.walk(sys.argv[1]):
    for n in dirs + files:
        p = os.path.join(d, n)
        if os.lstat(p).st_mode & 0o077:
            print(p)
PY
}

# 1
begin docker-not-running
export STUB_DOCKER_UP=0
run_script --scene sports --no-capture
expect_rc 1
expect_err "docker is not running"
bad="$(grep -v '^info' "$STUB_LOG" || true)"
[ -z "$bad" ] || fail "docker was called beyond the probe: $bad"
pass

# 2
begin port-busy
python3 -m http.server "$DEMO_PORT" --bind 127.0.0.1 > /dev/null 2>&1 &
LISTENER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
  ( exec 3<> "/dev/tcp/127.0.0.1/$DEMO_PORT" ) 2> /dev/null && break
  sleep 0.5
done
run_script --scene sports --no-capture
kill "$LISTENER_PID" 2> /dev/null; LISTENER_PID=""
expect_rc 1
expect_err "port $DEMO_PORT is already in use"
expect_no_call "network create"
[ ! -e "$DEMO_WORK" ] || fail "a refused run left $DEMO_WORK behind"
pass

# 3
begin leftover-data-refused
mkdir -p "$DEMO_WORK/data"
run_script --scene sports --no-capture
expect_rc 1
expect_err -- "--down"
expect_err -- "--reset"
expect_no_call "network create"
pass

# 4
begin rerun-cleans-first
mkdir -p "$DEMO_WORK/data"
run_script --scene sports --no-capture --reset
net_line="$(grep -n 'network create --internal' "$STUB_LOG" | head -n 1 | cut -d: -f1)"
[ -n "$net_line" ] || fail "no 'network create --internal' in the log"
for suffix in server recv proxy; do
  rm_line="$(grep -n "rm -f polyemesis-demo.*-$suffix\$" "$STUB_LOG" | head -n 1 | cut -d: -f1)"
  [ -n "$rm_line" ] || fail "no 'rm -f' of the -$suffix container before the network was created"
  [ "$rm_line" -lt "$net_line" ] || fail "-$suffix was removed after the network was created"
done
pass

# 5
begin slow-publisher-refused
progress() { # <file> <speed> <drops>
  : > "$1"
  for _ in 1 2 3; do
    printf 'frame=100\nfps=60.00\nspeed=%sx\ndrop_frames=%s\nprogress=continue\n' "$2" "$3" >> "$1"
  done
}
progress "$TMP/p-slow" 0.80 0
progress "$TMP/p-good" 1.00 0
progress "$TMP/p-drop" 1.00 3
( . "$LIB"; DEMO_FPS=60 publisher_speed_ok "$TMP/p-slow" ) > "$OUT" 2> "$ERR"; RC=$?
expect_rc 1
expect_err "DEMO_FPS=30"
( . "$LIB"; publisher_speed_ok "$TMP/p-good" ) > "$OUT" 2> "$ERR"; RC=$?
expect_rc 0
( . "$LIB"; publisher_speed_ok "$TMP/p-drop" ) > "$OUT" 2> "$ERR"; RC=$?
expect_rc 1
expect_err "dropped 3"
pass

# 6
begin real-host-refused-before-docker
python3 - "$ROOT/scripts/demo-seed.sports.fixture.json" "$TMP/real-host.json" <<'PY'
import json, sys
fx = json.load(open(sys.argv[1]))
fx["sources"][0]["destinations"][0]["url"] = "rtmp://a.rtmp.youtube.com/live2"
json.dump(fx, open(sys.argv[2], "w"))
PY
DEMO_FIXTURE="$TMP/real-host.json" run_script --scene sports --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "network create"
expect_no_call "run"
pass

# 7
begin no-release-tag-refused
env -u DEMO_VERSION PATH="$TMP/bin:$PATH" DEMO_REUSE_IMAGE=1 \
  bash "$SCRIPT" --scene sports > "$OUT" 2> "$ERR" < /dev/null
RC=$?
expect_rc 1
expect_err "no release tag"
expect_no_call "network create"
# The full scene refuses the same way, and a run that will not capture does not.
env -u DEMO_VERSION PATH="$TMP/bin:$PATH" DEMO_REUSE_IMAGE=1 \
  bash "$SCRIPT" > "$OUT" 2> "$ERR" < /dev/null
RC=$?
expect_rc 1
expect_err "no release tag"
expect_no_call "network create"
env -u DEMO_VERSION PATH="$TMP/bin:$PATH" DEMO_REUSE_IMAGE=1 \
  bash "$SCRIPT" --scene sports --no-capture > "$OUT" 2> "$ERR" < /dev/null
! grep -q "no release tag" "$ERR" || fail "--no-capture was refused for the missing tag"
pass

# 8
begin password-not-printed
SENTINEL="sentinel-pass-123"
E2E_PASSWORD="$SENTINEL" run_script --scene sports --no-capture
[ -f "$DEMO_WORK/admin.pw" ] || fail "the run never reached the credential file"
for f in admin.pw setup.code; do
  mode="$(mode_of "$DEMO_WORK/$f")"
  [ "$mode" = 600 ] || fail "$f has mode $mode, want 600"
done
open="$(open_paths "$DEMO_WORK")"
[ -z "$open" ] || fail "readable by group or others: $open"
grep -q "$SENTINEL" "$DEMO_WORK/admin.pw" || fail "admin.pw does not hold the password"
leak="$(grep -rl "$SENTINEL" "$OUT" "$ERR" "$STUB_LOG" "$DEMO_WORK" 2> /dev/null | grep -v '/admin\.pw$' || true)"
[ -z "$leak" ] || fail "the password reached: $leak"
pass

# 9
begin filters-match-generator
python3 "$ROOT/scripts/demo-sports/gen-filter.py" --check > "$OUT" 2> "$ERR"; RC=$?
expect_rc 0
command cp -R "$ROOT/scripts/demo-sports" "$TMP/demo-sports-copy"
printf 'x' >> "$TMP/demo-sports-copy/match.filter"
python3 "$TMP/demo-sports-copy/gen-filter.py" --check > "$OUT" 2> "$ERR"; RC=$?
expect_rc 1
pass

# 10
begin options-refused
run_script --scene bogus
expect_rc 2
run_script --state status
expect_rc 2
run_script --scene sports --state bogus
expect_rc 2
expect_no_call "network create"
pass

# 11
begin plan-line-keeps-empty-fields
printf 'abc\tsecret-token-xyz\t\t9000\t\n' > "$TMP/plan.tsv"
got="$( . "$LIB"; sports_plan_token "$TMP/plan.tsv" )"
[ "$got" = secret-token-xyz ] || fail "the token read from a line with empty fields was \"$got\""
pass

# 12
begin down-always-removes-the-work-directory
mkdir -p "$TMP/bin-dead"
printf '#!/bin/sh\nexit 1\n' > "$TMP/bin-dead/docker"
chmod +x "$TMP/bin-dead/docker"
for scene_args in "" "--scene sports"; do
  mkdir -p "$DEMO_WORK/data"
  printf x > "$DEMO_WORK/admin.pw"
  # shellcheck disable=SC2086
  PATH="$TMP/bin-dead:$PATH" bash "$SCRIPT" $scene_args --down > "$OUT" 2> "$ERR" < /dev/null
  RC=$?
  expect_rc 0
  [ ! -e "$DEMO_WORK" ] || fail "--down ${scene_args} left $DEMO_WORK behind"
done
pass

# 13
begin network-not-internal-refused
export STUB_NET_INTERNAL=false
run_script --scene sports --no-capture
unset STUB_NET_INTERNAL
expect_rc 1
expect_err "REFUSING"
expect_no_call "run"
# A rig that is already up is refused too, before anything acts on it.
mkdir -p "$DEMO_WORK"; printf x > "$DEMO_WORK/admin.pw"
: > "$STUB_LOG"
STUB_NET_INTERNAL=false run_script --scene sports --state start
expect_rc 1
expect_err "REFUSING"
for st in routing-final routing-initial; do
  STUB_NET_INTERNAL=false run_script --scene sports --state "$st"
  expect_rc 1
done
# So is --proof (U4): it reads from inside the network.
: > "$STUB_LOG"
STUB_NET_INTERNAL=false run_script --scene sports --proof
expect_rc 1
expect_err "REFUSING"
expect_no_call "run"
pass

# 14
begin plan-bitrate-is-field-4
printf 'abc\tsecret-token-xyz\t\t9000\t\n' > "$TMP/plan.tsv"
got="$( . "$LIB"; sports_plan_field "$TMP/plan.tsv" 4 )"
[ "$got" = 9000 ] || fail "field 4 of a line with empty fields was \"$got\""
# shellcheck disable=SC2016 # a literal search for the text
grep -q 'sports_plan_field "\$1" 4' "$LIB" || fail "sports_publish does not read the bitrate from field 4"
! grep -q '9000k' "$LIB" || fail "the bitrate is hard-coded in the library"
pass

# 15
begin full-fixture-alert-urls-pass
go run "$ROOT/scripts/demo_seed_driver.go" - guardcontent "$ROOT/scripts/demo-seed.fixture.json" > "$OUT" 2> "$ERR"; RC=$?
expect_rc 0
python3 - "$ROOT/scripts/demo-seed.fixture.json" <<'PY' || fail "the fixture is missing a section the full scene seeds"
import json, sys
f = json.load(open(sys.argv[1]))
assert len(f["automation"]["alertRules"]) >= 3 and len(f["automation"]["schedules"]) >= 3
assert len(f["settings"]["automod"]["rules"]) >= 2
assert f["clips"]["windowSeconds"] > 0 and "hooks" not in f["automation"]
PY
pass

# 16
begin real-alert-url-refused-before-anything-starts
python3 - "$ROOT/scripts/demo-seed.fixture.json" "$TMP/bad.fixture.json" <<'PY'
import json, sys
f = json.load(open(sys.argv[1]))
f["automation"]["alertRules"][0]["url"] = "https://hooks.slack.com/services/T0/B0/x"
json.dump(f, open(sys.argv[2], "w"))
PY
DEMO_FIXTURE="$TMP/bad.fixture.json" run_script --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "network create"
expect_no_call "run"
pass

# mutate_full <name> <python statement on f>: a copy of the full fixture with
# one change, run through the full scene. It must refuse before any container
# or network exists.
mutate_full() {
  python3 - "$ROOT/scripts/demo-seed.fixture.json" "$TMP/mutant.$N.json" "$1" <<'PY'
import json, sys
f = json.load(open(sys.argv[1]))
d0 = f["sources"][0]["destinations"][0]
exec(sys.argv[3])
json.dump(f, open(sys.argv[2], "w"))
PY
  DEMO_FIXTURE="$TMP/mutant.$N.json" run_script --no-capture
  expect_rc 1
  expect_err "REFUSING"
  expect_no_call "network create"
  expect_no_call "run"
}

# 17
begin full-real-destination-refused-before-anything-starts
mutate_full 'd0["url"] = "rtmp://a.rtmp.youtube.com/live2"; d0["enabled"] = True'
pass

# 18
begin full-real-backup-url-refused
mutate_full 'd0["backupUrl"] = "rtmp://b.rtmp.youtube.com/live2"'
pass

# 19
begin full-start-schedule-with-no-targets-refused
mutate_full 'f["automation"]["schedules"][3]["destinations"] = []; f["automation"]["schedules"][3]["enabled"] = True'
pass

# 20
begin full-start-schedule-on-a-network-destination-refused
mutate_full 'f["automation"]["schedules"][3]["destinations"] = [d0["name"]]'
pass

# 21
begin full-destination-with-platform-fields-refused
mutate_full 'd0["extraOutputArgs"] = "-f flv x"'
mutate_full 'd0["accountId"] = 3'
mutate_full 'd0["multitrack"] = True'
pass

# 22
begin full-fixture-destinations-pass
go run "$ROOT/scripts/demo_seed_driver.go" - guardcontent "$ROOT/scripts/demo-seed.fixture.json" > "$OUT" 2> "$ERR"; RC=$?
expect_rc 0
pass

# 23
begin full-network-not-internal-refused
STUB_NET_INTERNAL=false run_script --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "run"
# Nothing created before the check that the full scene's network is internal.
grep -q 'network create --internal' "$STUB_LOG" || fail "the full scene did not create an --internal network"
pass

# 24
begin sports-fixture-platform-fields-refused
# One change per run, each on a copy of the sports fixture (U3): a real-host
# backupUrl, extra output args, extra input args, an account id, multitrack.
for change in \
  'd["backupUrl"] = "rtmp://b.rtmp.youtube.com/live2"' \
  'd["extraOutputArgs"] = "-f flv x"' \
  'd["extraInputArgs"] = "-re"' \
  'd["accountId"] = 3' \
  'd["multitrack"] = True'; do
  python3 - "$ROOT/scripts/demo-seed.sports.fixture.json" "$TMP/sports-mutant.json" "$change" <<'PY'
import json, sys
fx = json.load(open(sys.argv[1]))
d = fx["sources"][0]["destinations"][0]
exec(sys.argv[3])
json.dump(fx, open(sys.argv[2], "w"))
PY
  DEMO_FIXTURE="$TMP/sports-mutant.json" run_script --scene sports --no-capture
  expect_rc 1
  expect_err "REFUSING"
  expect_no_call "network create"
  expect_no_call "run"
done
pass

# 25
begin sports-enabled-destination-refused
python3 - "$ROOT/scripts/demo-seed.sports.fixture.json" "$TMP/sports-enabled.json" <<'PY'
import json, sys
fx = json.load(open(sys.argv[1]))
fx["sources"][0]["destinations"][0]["enabled"] = True
json.dump(fx, open(sys.argv[2], "w"))
PY
DEMO_FIXTURE="$TMP/sports-enabled.json" run_script --scene sports --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "network create"
pass

# 26
begin server-publishing-a-port-refused
# The server container answers `docker port` with a line: the relay is not
# started, because only the relay may publish (U5).
STUB_SERVER_OK=1 STUB_PORT_LINE="8097/tcp -> 0.0.0.0:8097" run_script --scene sports --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "socat"
pass

# 27
begin full-case-variant-keys-refused
# The server matches JSON keys ignoring case, so a variant is a real field to it.
mutate_full 'd0["backupurl"] = "rtmp://b.rtmp.youtube.com/live2"'
mutate_full 'd0["Multitrack"] = True'
mutate_full 'd0["multitracK"] = True'
mutate_full 'd0["Enabled"] = True'
mutate_full 's = f["automation"]["schedules"][3]; del s["action"]; s["Action"] = "start"; s["destinations"] = []; s["enabled"] = True'
pass

# 28
begin sports-case-variant-enabled-refused
python3 - "$ROOT/scripts/demo-seed.sports.fixture.json" "$TMP/sports-variant.json" <<'PY'
import json, sys
fx = json.load(open(sys.argv[1]))
d = fx["sources"][0]["destinations"][0]
del d["enabled"]
d["Enabled"] = True
json.dump(fx, open(sys.argv[2], "w"))
PY
DEMO_FIXTURE="$TMP/sports-variant.json" run_script --scene sports --no-capture
expect_rc 1
expect_err "REFUSING"
expect_no_call "network create"
expect_no_call "run"
pass

# 29
begin full-enabled-network-destination-refused
mutate_full 'd0["enabled"] = True'
pass

# 30
begin full-settings-host-refused
mutate_full 'f["settings"]["mqtt"] = {"brokerUrl": "tcp://broker.example.com:1883"}'
mutate_full 'f["settings"]["recording"]["Enabled"] = True'
pass

# 31
begin docker-engine-too-old-refused
# Engines before 26.0.0 forward DNS from an internal network to the host.
STUB_DOCKER_VERSION=25.0.5 run_script --scene sports --no-capture
expect_rc 1
expect_err "REFUSING"
expect_err "26"
expect_no_call "run"
STUB_DOCKER_VERSION=25.0.5 run_script --no-capture --reset
expect_rc 1
expect_err "REFUSING"
expect_no_call "run"
: > "$STUB_LOG"
STUB_DOCKER_VERSION=26.0.0 run_script --scene sports --no-capture --reset
! grep -q "older than 26" "$ERR" || fail "26.0.0 was refused as too old"
pass
