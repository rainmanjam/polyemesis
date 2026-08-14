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
# NEEDS FFMPEG 7.1+ ON THE HOST, and step 1 refuses rather than assumes. Below
# that, multitrack FLV does not demux and this suite would report the one track
# it asserts no matter what OBS sent. See the round-trip probe there.
#
# WHERE IT RUNS: weekly, and on pull requests that touch the RTMP listener, the
# OBS image or this file — .github/workflows/obs-multitrack.yml says why.
#
# Usage:  ./scripts/acceptance-obs-multitrack.sh [workdir]
#
#   TRACKS=3          audio tracks to configure OBS with
#   OBS_SENDS=1       the ratchet: how many OBS is known to actually send
#   CONNECT_WAIT=60   seconds to wait for OBS to dial in (26.8s measured local)
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

# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they hold the ports the NEXT run has to bind.
#
# THIS SUITE WAS THE ODD ONE OUT, and running it twice back to back is what
# showed it. The old trap signalled the server and returned immediately, so run
# two started while run one's process was still finalising:
#
#   msg="one-port ingest could not start" proto=rtmp addr=:1935
#     err="rtmp listen :1935: listen tcp :1935: bind: address already in use"
#
# and the suite reported "the RTMP listener never bound" — a teardown bug
# wearing the costume of the exact product failure this suite exists to detect.
# lib-cleanup.sh's header describes this precise symptom in acceptance-failover
# (10 failures in 20 back-to-back runs) and was written to end it; thirteen
# suites adopted it and this one did not. A signal is a request, not an
# observation.
. "$SCRIPTS/lib-cleanup.sh"
cleanup() {
  # The container first, and unconditionally: it is the one piece of this run's
  # state that does not live in a process table poly_cleanup can sweep.
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  # INGEST is passed because the ingest ffmpeg's argv carries no work-dir path,
  # so the sweep cannot see it. It is the port that leaked.
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK" "$INGEST"
}
trap 'poly_teardown_trap $? cleanup' EXIT

command -v docker >/dev/null || { echo "docker is required"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running"; exit 1; }

# Built here, not assumed. The failover suite used to run whatever binary was
# lying in the repo root and passed against code from hours earlier; the capture
# script did the same with a week-old container image. Both hid real bugs.
step "1. Build"
go build -o "$BIN" "$ROOT/cmd/polyemesis" || { echo "cannot build polyemesis"; exit 1; }
ok "polyemesis built"

# CAN THIS HOST SEE MULTITRACK AT ALL? Measured before anything else, because
# every later result is worth exactly nothing without it.
#
# Step 4 asserts a NEGATIVE — that one audio track arrives — and a negative is
# only worth as much as the observer's ability to have seen more. Multitrack
# FLV demuxing landed in FFmpeg 7.1. Ubuntu 24.04 ships 6.1.1, which is what
# `apt-get install ffmpeg` gives on a GitHub runner and on the OVH deployment,
# and it reads a multitrack stream as one track without erroring.
#
# So on 6.1.1 this suite probes 1, matches OBS_SENDS=1, and reports a pass — on
# a host that could not have reported anything else. The day OBS gains
# multitrack audio this suite is the only thing that would notice, and that is
# precisely the day it would go quietly green instead. The old code knew: it
# printed "on FFmpeg below 7.1 this reads as 1" from the tracks-were-LOST
# branch, which at OBS_SENDS=1 can never be reached. The warning was correct
# and unreachable.
#
# A ROUND TRIP, NOT A VERSION STRING. Two sine tracks are muxed into FLV and
# demuxed back, and the count is read from what came out. Measured: 8.1.2
# returns 2; 6.1.1 refuses the mux with "at most one audio stream is supported
# in flv" and returns 0. That measures the capability this suite depends on
# rather than a number somebody has to map onto it.
command -v ffmpeg >/dev/null && command -v ffprobe >/dev/null \
  || { echo "ffmpeg and ffprobe are required on the host"; exit 1; }
MT=$(mktemp -d)
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i sine=f=300:d=0.3 -f lavfi -i sine=f=500:d=0.3 \
  -map 0:a -map 1:a -c:a aac -f flv -y "$MT/mt.flv" > "$MT/err" 2>&1
SEEN=$(ffprobe -hide_banner -loglevel error -select_streams a \
         -show_entries stream=index -of csv=p=0 "$MT/mt.flv" 2>/dev/null | wc -l | tr -d ' ')
if [[ "$SEEN" == "2" ]]; then
  ok "host FFmpeg can demux multitrack FLV — $(ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f1-3)"
else
  bad "host FFmpeg cannot demux multitrack FLV: a 2-track round trip came back as $SEEN"
  note "$(head -1 "$MT/err" 2>/dev/null)"
  note "Multitrack FLV demuxing landed in FFmpeg 7.1, and this build predates it."
  note "This suite asserts OBS sends exactly ONE track; on this FFmpeg it would"
  note "report one whatever OBS sent, so the assertion would be vacuous."
  note "REFUSING to report a pass this run could not have earned."
  note "host ffmpeg: $(ffmpeg -version 2>/dev/null | head -1)"
  rm -rf "$MT"
  exit 1
fi
rm -rf "$MT"

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
# No PID kept. The teardown finds this server by its argv, the same way
# poly_stop_server does for every other suite, and a remembered PID would only
# offer a second way to signal it that does not WAIT for it to go.
"$BIN" -addr ":$PORT" -data "$DATA" -log info > server.log 2>&1 &
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
  || { bad "the RTMP listener never bound"; note "$(grep -m2 'could not start' server.log)"; exit 1; }

# THE LOG LINE IS A CLAIM; THE SOCKET IS THE OBSERVATION, and only one of them
# is what OBS is about to dial. lib-cleanup.sh states the general case — "the
# server said it is ready is not the ingest is accepting connections" — and
# here the gap is paid for by a container that takes ~27 seconds to reach its
# RTMP connect. If it lands in that window it gets refused, exits, and the
# suite blames OBS for a handshake that was never offered one.
poly_wait_port_ready "$INGEST" 20 \
  && ok "the shared RTMP listener is bound on :$INGEST and accepting" \
  || { bad "the RTMP listener logged ready but nothing holds :$INGEST"; exit 1; }

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

# A BUDGET, AND A VARIABLE ONE, because this wait is not bounded by the network
# — it is bounded by how long a GUI application takes to start.
#
# Measured on an Apple Silicon laptop: the listener binds, and OBS's publisher
# connects 26.8 seconds later. That is Xvfb, then OBS's own startup, then a
# scene composited through llvmpipe in software, before a single RTMP byte is
# sent. 60 leaves less headroom than it looks — 2.2x measured, on the fastest
# hardware this runs on. A 4-vCPU hosted runner doing the same software
# rasterisation has no reason to match it, so CI raises this rather than
# discovering the ceiling as a flake and reading it as "OBS was refused".
CONNECT_WAIT="${CONNECT_WAIT:-60}"
if grep -q "rtmp publisher connected" <(timeout "$CONNECT_WAIT" tail -f server.log); then
  ok "OBS's publisher was ADMITTED by the shared listener"
else
  bad "OBS never connected in ${CONNECT_WAIT}s — refused, or too slow to start"
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
