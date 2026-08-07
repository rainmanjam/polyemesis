#!/usr/bin/env bash
# OBS acceptance: does what REAL OBS publishes arrive intact?
#
# This is the run docs/notes/enhanced-rtmp-multitrack.md listed as the last
# unconfirmed thing, and it went looking for one answer and found another.
#
# WHAT IT CONFIRMED. OBS's own RTMP connect and handshake are accepted by the
# shared listener, its stream key is admitted, and what it sends is probed and
# decodable. That is the part FFmpeg-as-publisher could never stand in for, and
# it works.
#
# WHAT IT DISPROVED. OBS 30.2.3 does not send multitrack audio over RTMP at all,
# so "multitrack from OBS" could not be confirmed because OBS does not do it.
# See the pinned finding at step 4 for the evidence — captured bytes, not
# inference. The docs said 30.2+ sends it; that was read from OBS's muxer source,
# which does implement it, without checking whether anything reaches that code.
#
# OBS RUNS IN DOCKER, HEADLESS. See scripts/obs/Dockerfile: OBS is a GUI
# application with no batch mode, so it gets a virtual display and a software
# OpenGL driver, and --startstreaming makes it begin without anyone clicking.
#
# Usage:  ./scripts/acceptance-obs-multitrack.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-obs}"
PORT=8098
INGEST=1935          # must match ingestPort in acceptance_obs_driver.go
TRACKS="${TRACKS:-3}"
IMAGE=polyemesis-obs:local
CONTAINER=polyemesis-obs-publisher

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  pkill -f "polyemesis -addr :$PORT" 2>/dev/null
}
trap cleanup EXIT

command -v docker >/dev/null || { echo "docker is required"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running"; exit 1; }

# Built here, not assumed. The failover suite used to run whatever binary was
# lying in the repo root and passed against code from hours earlier; the capture
# script did the same with a week-old container image. Both hid real bugs.
step "1. Build"
go build -o "$BIN" "$ROOT/cmd/polyemesis" || { echo "cannot build polyemesis"; exit 1; }
ok "polyemesis built"
# The image is rebuilt for the same reason. Docker's layer cache makes this
# nearly free when nothing changed.
docker build -q -t "$IMAGE" "$SCRIPTS/obs" >/dev/null || { echo "cannot build $IMAGE"; exit 1; }
OBS_VER=$(docker run --rm --entrypoint bash "$IMAGE" -c 'obs --version 2>&1 | head -1')
ok "OBS image built — $OBS_VER"
# 30.2 is where OBS's FLV muxer gained the multitrack code path. Whether
# anything ever reaches it is the question step 4 answers; pinning the floor
# here keeps this suite measuring a known quantity rather than an arbitrary one.
case "$OBS_VER" in
  *"- 30.0"*|*"- 29."*|*"- 28."*)
    bad "OBS predates 30.2 and its muxer has no multitrack path at all"; exit 1 ;;
esac

rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
mkdir -p data/recordings
DATA="$WORK/data"

export E2E_PASSWORD="E2E-$(openssl rand -hex 16)"
DRIVER="$WORK/obs-driver"
go build -o "$DRIVER" "$SCRIPTS/acceptance_obs_driver.go" || { echo "cannot build the driver"; exit 1; }
drive() { "$DRIVER" "http://127.0.0.1:$PORT" "$@" 2>&1; }

step "2. Server, on RTMP ingest"
"$BIN" -addr ":$PORT" -data "$DATA" -log info > server.log 2>&1 &
SRV_PID=$!
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
grep -q "polyemesis" server.log || { bad "server did not start"; exit 1; }
ok "server started"

TOKEN=$(drive | tail -1)
case "$TOKEN" in
  ""|*" "*|*driver:*) bad "could not set up the source: $TOKEN"; exit 1 ;;
  *) ok "source on RTMP ingest, token read ($(printf %s "$TOKEN" | cut -c1-4)…)" ;;
esac

# The shared listener has to be bound before OBS dials it, or the encoder gets
# connection-refused and the failure looks like a codec problem.
for _ in $(seq 1 40); do
  grep -q "one-port rtmp ingest listening" server.log && break; sleep 0.5
done
grep -q "one-port rtmp ingest listening" server.log \
  && ok "the shared RTMP listener is bound on :$INGEST" \
  || { bad "the RTMP listener never bound"; exit 1; }

step "3. OBS publishes $TRACKS tracks"
# host.docker.internal resolves to the host from inside the container on Docker
# Desktop; --add-host makes it work on plain Linux Docker too.
docker run -d --name "$CONTAINER" \
  --add-host=host.docker.internal:host-gateway \
  -e SECONDS_TO_STREAM=45 \
  -e TRACKS="$TRACKS" \
  -e RTMP_URL="rtmp://host.docker.internal:$INGEST/live" \
  -e RTMP_KEY="$TOKEN" \
  "$IMAGE" >/dev/null || { bad "could not start the OBS container"; exit 1; }
ok "OBS container started"

if grep -q "rtmp publisher connected" <(timeout 60 tail -f server.log); then
  ok "OBS's publisher was ADMITTED by the shared listener"
else
  bad "OBS never connected — its RTMP handshake or key was refused"
  note "--- OBS log ---"; docker logs "$CONTAINER" 2>&1 | grep -viE 'keysym|xkbcomp' | tail -20
  note "--- server log ---"; tail -15 server.log
  exit 1
fi

step "4. What actually arrived"
if drive waitlive | grep -q LIVE; then
  ok "bytes reached the relay and the layout was probed"
else
  bad "OBS connected but nothing decodable arrived"
  note "--- OBS log ---"; docker logs "$CONTAINER" 2>&1 | grep -viE 'keysym|xkbcomp' | tail -20
  exit 1
fi

READ=$(drive tracks | tail -1)
COUNT=${READ%% *}
LAYOUT=${READ#* }
note "polyemesis probed: $COUNT track(s) — $LAYOUT"

# WHAT OBS ACTUALLY SENDS, pinned, because it is not what the docs assumed.
#
# OBS 30.2.3 configured with three audio tracks, each routed to its own mixer,
# with StreamMultiTrackAudioMixes=7 and a custom RTMP service, publishes ONE
# audio track. Not a probe artefact and not something polyemesis dropped:
# capturing OBS's bytes verbatim and walking the FLV tag headers gives
#
#     0xaf legacy  x3541
#
# and no 0x95 ExHeader Multitrack at all. The gate is in the service catalogue —
# rtmp-services.so tests `supports_additional_audio_track`, and NO service in
# services.json declares it (0 of 91). So the capability is unreachable in this
# version for every service, custom RTMP included.
#
# This is therefore a RATCHET, not a target. polyemesis must deliver exactly
# what OBS sent; the day OBS gains multitrack audio, this fails and says so,
# which is the notification we want rather than a suite that quietly keeps
# passing while the interesting thing changes.
OBS_SENDS=${OBS_SENDS:-1}

if [ "$COUNT" = "$OBS_SENDS" ]; then
  ok "polyemesis delivered exactly what OBS put on the wire ($COUNT track)"
  note "OBS 30.2.x sends ONE audio track regardless of configuration — see the"
  note "comment above. This run confirms fidelity, NOT multitrack."
elif [ "$COUNT" -lt "$OBS_SENDS" ] 2>/dev/null; then
  bad "polyemesis probed $COUNT of the $OBS_SENDS track(s) OBS sent — tracks were LOST"
  note "on FFmpeg below 7.1 this reads as 1: multitrack FLV demuxing landed in 7.1"
  note "host ffmpeg: $(ffmpeg -version 2>/dev/null | head -1)"
else
  bad "OBS now sends $COUNT tracks, not $OBS_SENDS — its multitrack gate has opened"
  note "This is GOOD NEWS and a real change: re-run the wire capture, then update"
  note "OBS_SENDS here, docs/FAQ.md and docs/notes/enhanced-rtmp-multitrack.md."
fi

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n\n" "$pass" "$fail"
if [ "$fail" -eq 0 ]; then
  printf "  \033[32mOBS MULTITRACK ACCEPTANCE PASSED\033[0m\n"; exit 0
fi
printf "  \033[31mOBS MULTITRACK ACCEPTANCE FAILED\033[0m\n"; exit 1
