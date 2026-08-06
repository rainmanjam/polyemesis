#!/usr/bin/env bash
# Recording finalisation on shutdown.
#
# deploy/polyemesis.service promises that polyemesis "tears its FFmpeg children
# down in order on SIGTERM so a recording is finalised rather than truncated".
# That promise was false on Linux, and nothing tested it.
#
# Manager.Stop used to call srt.Stop() -- which closes every established
# publisher -- BEFORE stopping the engines. The recorder is an FFmpeg reading
# udp://127.0.0.1:..., and an FFmpeg blocked in a read on a source that has gone
# quiet does not act on SIGTERM. It missed the 8s grace and was killed as a
# group, so the Matroska trailer was never written. Measured on a live host: the
# file did not grow by a single byte across the stop and ffprobe reported
# duration=N/A on nearly two minutes of footage.
#
# The distinguishing evidence, and what this suite reproduces: with the input
# still flowing the recorder exits in about a tenth of a second and writes its
# trailer; with the input already silent it never exits at all. So the assertion
# is not "the file exists" -- a truncated file exists too, at exactly the right
# size. It is that the container is FINALISED: a real duration, a real bitrate,
# and a clean decode from end to end.
#
# Usage:  ./scripts/acceptance-recording-stop.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-recstop}"
PORT=8096
SRT_PORT=6096
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"
. "$SCRIPTS/lib-watchdog.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-recstop-source" 2>/dev/null
  poly_cleanup "$PORT" "$WORK"
}
trap 'poly_watchdog_disarm; cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
ffmpeg -hide_banner -protocols 2>/dev/null | tr ' ' '\n' | grep -qx srt || {
  echo "this suite needs an FFmpeg with libsrt"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
poly_watchdog_arm

PW='RecStop-9x!Qz2#pLm'
API="http://127.0.0.1:$PORT/api/v1"
CJ="$WORK/cookies"
csrf() { awk '/polyemesis_csrf/{print $7}' "$CJ"; }

step "1. Server and first-run setup"
"$BIN" -addr ":$PORT" -data ./data -log info > server.log 2>&1 &
for _ in $(seq 1 40); do
  curl -fsS -m2 "$API/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -fsS -m5 "$API/health" >/dev/null 2>&1 \
  && ok "server started" || { bad "server did not start"; tail -20 server.log; exit 1; }

curl -fsS -m5 -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" "$API/setup" >/dev/null 2>&1 \
  && ok "first-run setup" || { bad "setup failed"; exit 1; }
curl -fsS -m5 -c "$CJ" -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PW\"}" "$API/auth/login" >/dev/null 2>&1 \
  && ok "signed in" || { bad "login failed"; exit 1; }

step "2. Recording on, ingest on a port of our own"
# The SRT port is moved off the default so this suite can run beside a real
# install and beside the other suites, which is the same reason every suite here
# picks its own HTTP port.
curl -fsS -m5 -b "$CJ" "$API/settings" > settings.json 2>/dev/null || { bad "could not read settings"; exit 1; }
python3 - "$SRT_PORT" <<'PY' || { echo "could not build the settings document"; exit 1; }
import json, sys
s = json.load(open("settings.json"))
s["recording"]["enabled"] = True
s["listeners"]["srtPort"] = int(sys.argv[1])
json.dump(s, open("settings.new.json", "w"))
PY
curl -fsS -m10 -b "$CJ" -X PUT -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(csrf)" --data @settings.new.json "$API/settings" >/dev/null 2>&1 \
  && ok "recording enabled" || { bad "could not enable recording"; exit 1; }

TOKEN=$(curl -fsS -m5 -b "$CJ" "$API/sources" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["token"])' 2>/dev/null)
[ -n "$TOKEN" ] && ok "got the publish token" || { bad "no publish token"; exit 1; }

step "3. Publish, and wait for a recording to actually grow"
ffmpeg -hide_banner -loglevel error -re \
  -f lavfi -i "testsrc2=size=320x180:rate=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -map 0:v -map 1:a \
  -c:v libx264 -preset ultrafast -tune zerolatency -b:v 600k -pix_fmt yuv420p \
  -c:a aac -b:a 96k -metadata title=acceptance-recstop-source \
  -t 120 -f mpegts \
  "srt://127.0.0.1:$SRT_PORT?streamid=$TOKEN&mode=caller&transtype=live&latency=200000" \
  > source.log 2>&1 &

REC=""
for _ in $(seq 1 60); do
  REC=$(ls -t ./data/recordings/*.mkv 2>/dev/null | head -1)
  # Not just "a file exists" -- a segment file is created before anything is
  # written into it, and stopping at that instant would pass a test of nothing.
  [ -n "$REC" ] && [ "$(stat -c %s "$REC" 2>/dev/null || echo 0)" -gt 500000 ] && break
  sleep 1
done
[ -n "$REC" ] && ok "a recording is being written ($(stat -c %s "$REC") bytes)" \
  || { bad "no recording appeared; the ingest never went live"; tail -20 server.log; exit 1; }

BEFORE=$(stat -c %s "$REC")

step "4. Stop the server mid-recording"
SRV=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
[ -n "$SRV" ] || { bad "could not find the server process"; exit 1; }
T0=$(date +%s.%N)
kill -TERM "$SRV"
for _ in $(seq 1 120); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.25; done
T1=$(date +%s.%N)
ELAPSED=$(echo "$T1 - $T0" | bc)

# Before the fix this was a deterministic ~16s: two 8s supervisor grace periods
# back to back, because no child exited on SIGTERM and all of them were killed.
if [ "$(echo "$ELAPSED < 8" | bc)" = "1" ]; then
  ok "shutdown took ${ELAPSED}s, inside one grace period"
else
  bad "shutdown took ${ELAPSED}s — a child was waited out and killed"
fi

if grep -q "did not exit after grace period" server.log; then
  bad "a child had to be SIGKILLed: $(grep -c 'did not exit after grace period' server.log) of them"
else
  ok "every child exited on SIGTERM"
fi

step "5. The recording is finalised, not merely present"
AFTER=$(stat -c %s "$REC")
# The trailer is the whole point. A truncated file is exactly the size it was
# when the process died, which is why size-unchanged is the failure signature.
if [ "$AFTER" -gt "$BEFORE" ]; then
  ok "a trailer was written (grew $((AFTER-BEFORE)) bytes during shutdown)"
else
  bad "the file did not grow across the stop — no trailer, so it was truncated"
fi

DUR=$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$REC" </dev/null 2>/dev/null)
case "$DUR" in
  ""|N/A) bad "ffprobe reports no duration — the container was never finalised" ;;
  *) if [ "$(echo "$DUR > 1" | bc 2>/dev/null)" = "1" ]; then
       ok "ffprobe reports a real duration (${DUR}s)"
     else
       bad "ffprobe duration is $DUR, which is not a recording"
     fi ;;
esac

RATE=$(ffprobe -v error -show_entries format=bit_rate -of default=nw=1:nk=1 "$REC" </dev/null 2>/dev/null)
case "$RATE" in
  ""|N/A) bad "ffprobe reports no bitrate — the index is missing" ;;
  *) ok "ffprobe reports a real bitrate (${RATE} bps)" ;;
esac

# The strongest assertion here: decode the whole file. A truncated Matroska
# reports "File ended prematurely" on the way through even when its header
# looks plausible.
DECODE=$(ffmpeg -v warning -i "$REC" -f null - </dev/null 2>&1)
if [ -z "$DECODE" ]; then
  ok "decodes end to end with no warnings"
else
  bad "decode complained: $(echo "$DECODE" | head -1)"
fi

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n\n" "$pass" "$fail"
[ "$fail" -eq 0 ] && { echo "  ACCEPTANCE PASSED"; exit 0; }
echo "  ACCEPTANCE FAILED"; exit 1
