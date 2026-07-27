#!/usr/bin/env bash
# Container acceptance test.
#
# The other six suites drive a binary built on this machine. They cannot fail
# when the IMAGE is broken while the code is fine — and that is a real failure
# mode with real history here: the Go stage was once pinned below go.mod's
# floor, and the runtime stage depends on a base image whose FFmpeg could drop
# SRT at any rebuild. This suite exists to catch those.
#
# It builds the shipped Dockerfile, boots it, drives the same REST API the web
# UI uses, publishes real multitrack SRT and RTMP into the container, and then
# proves per-destination routing by MEASURING the audio that came out rather
# than by trusting a log line.
#
# Usage:  ./scripts/acceptance-docker.sh
# Requires: docker (with a running daemon), go
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$SCRIPTS/acceptance_docker_driver.go"

IMAGE=polyemesis:acceptance
CTR=poly-acc
NET=poly-acc-net
VOL=poly-acc-data
PORT=8099
SRTPORT=6099
RTMPPORT=1999
BASE="http://127.0.0.1:$PORT"

pass=0; fail=0; skip=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
sk()   { printf "  \033[33mSKIP\033[0m  %s\n" "$1"; skip=$((skip+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

cleanup() {
  docker rm -f "$CTR" pub-acc >/dev/null 2>&1
  docker volume rm "$VOL" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

drive() { go run "$DRIVER" "$1" "$BASE" "${2:-}" 2>&1; }
inctr() { docker exec "$CTR" sh -c "$1" 2>/dev/null; }

# Publish a synthetic multitrack stream INTO the container. Three audio tracks
# at well-separated frequencies so each one can be identified afterwards by
# bandpass measurement; that is what makes "track 2 reached this destination"
# a measurement instead of an assertion.
publish() { # publish <url> <seconds>
  docker run -d --rm --name pub-acc --network "$NET" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=300:sample_rate=48000" \
    -f lavfi -i "sine=frequency=1200:sample_rate=48000" \
    -f lavfi -i "sine=frequency=5000:sample_rate=48000" \
    -map 0:v -map 1:a -map 2:a -map 3:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v 1200k \
    -c:a aac -b:a 128k -ac 2 -t "$2" -f mpegts "$1" >/dev/null 2>&1
}

# RTMP carries ONE audio track by protocol, so this publisher is deliberately
# single-track: a 3-track RTMP publish would fail for reasons that have nothing
# to do with polyemesis.
publish_rtmp() {
  docker run -d --rm --name pub-acc --network "$NET" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=300:sample_rate=48000" \
    -map 0:v -map 1:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v 1200k \
    -c:a aac -b:a 128k -ac 2 -t "$2" -f flv "$1" >/dev/null 2>&1
}

# rms <file> <freq> <width> -> RMS dBFS inside that band.
rms() {
  inctr "cd /data/recordings && ffmpeg -hide_banner -nostats -i $1 \
    -af 'bandpass=f=$2:width_type=h:w=$3,astats=metadata=1:reset=0' -f null - 2>&1 \
    | grep 'RMS level dB' | tail -1 | sed 's/.*: *//'"
}

# Integer comparison on a dB figure, which is a float and may be negative.
louder_than() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 > b+0)}'; }

command -v docker >/dev/null || { echo "docker not found"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker daemon not running"; exit 1; }

# ------------------------------------------------------------------ 1. image
step "1. Image"
if docker build -t "$IMAGE" --build-arg VERSION=acceptance "$ROOT" >/tmp/poly-acc-build.log 2>&1; then
  ok "shipped Dockerfile builds"
else
  bad "shipped Dockerfile builds (see /tmp/poly-acc-build.log)"
  tail -15 /tmp/poly-acc-build.log
  exit 1
fi

FFV=$(docker run --rm --entrypoint ffmpeg "$IMAGE" -hide_banner -version 2>/dev/null | head -1 | awk '{print $3}')
case "$FFV" in
  6.*|7.*|8.*) ok "ffmpeg $FFV is >= 6" ;;
  *)           bad "ffmpeg version unusable: '$FFV'" ;;
esac

# Whole-token match. `grep srt` is no check at all: every build lists srtp,
# which is Secure RTP and cannot carry an ingest.
if docker run --rm --entrypoint sh "$IMAGE" -c 'ffmpeg -hide_banner -protocols | tr " " "\n" | grep -qx srt'; then
  ok "SRT protocol present (whole-token match, not srtp)"
else
  bad "SRT protocol MISSING — multitrack ingest cannot work"
fi
docker run --rm --entrypoint sh "$IMAGE" -c 'ffmpeg -hide_banner -protocols | tr " " "\n" | grep -qx rtmp' \
  && ok "RTMP protocol present" || bad "RTMP protocol missing"
docker run --rm --entrypoint sh "$IMAGE" -c 'ffmpeg -hide_banner -encoders 2>/dev/null | grep -q " libx264 "' \
  && ok "libx264 encoder present" || bad "libx264 encoder missing"
docker run --rm --entrypoint sh "$IMAGE" -c 'ffmpeg -hide_banner -encoders 2>/dev/null | grep -qE "^ A....D aac "' \
  && ok "aac encoder present" || bad "aac encoder missing"

UID_=$(docker run --rm --entrypoint id "$IMAGE" -u 2>/dev/null)
[ "$UID_" = "10001" ] && ok "runs as non-root (uid 10001)" || bad "runs as uid '$UID_', expected 10001"

# ------------------------------------------------------------------ 2. boot
step "2. Boot"
docker network create "$NET" >/dev/null 2>&1
docker volume create "$VOL" >/dev/null 2>&1
docker run -d --name "$CTR" --network "$NET" \
  -p "$PORT:8080" -p "$SRTPORT:6000/udp" -p "$RTMPPORT:1935" \
  -v "$VOL:/data" "$IMAGE" >/dev/null 2>&1

healthy=no
for _ in $(seq 1 60); do
  if [ "$(docker inspect --format '{{.State.Health.Status}}' "$CTR" 2>/dev/null)" = "healthy" ]; then
    healthy=yes; break
  fi
  sleep 1
done
[ "$healthy" = yes ] && ok "container reports healthy" || bad "container never became healthy"

# grep -c, never grep -q, on a pipeline from docker logs. Under `set -o
# pipefail` a `grep -q` that MATCHES exits immediately, docker logs takes
# SIGPIPE, and the pipeline reports failure — so the check fails exactly when
# it should pass. grep -c consumes all of its input and cannot do that.
logmatch() { docker logs "$CTR" 2>&1 | grep -cE "$1" | tr -d ' '; }

if [ "$(logmatch 'msg="ffmpeg detected".*srt=true')" != "0" ]; then
  ok "polyemesis detects its own FFmpeg with SRT"
else
  bad "polyemesis did not detect SRT in its bundled FFmpeg"
fi

if [ "$(logmatch 'level=ERROR')" != "0" ]; then
  bad "ERROR lines at boot"
else
  ok "no ERROR lines at boot"
fi

inctr 'touch /data/.wtest && rm /data/.wtest' && ok "/data writable by the non-root user" \
  || bad "/data NOT writable by the non-root user"

# ------------------------------------------------------------------ 3. auth
step "3. First run and access control"
SETUP=$(drive setup)
case "$SETUP" in
  *SETUP_OK*)         ok "first-run setup creates the admin, and refuses to run twice" ;;
  *SETUP_REPEATABLE*) bad "setup can be replayed — an exposed port is a takeover" ;;
  *)                  bad "setup failed: $SETUP" ;;
esac

SEC=$(drive security)
case "$SEC" in
  *ANON_REFUSED*)  ok "unauthenticated mutation refused" ;;
  *)               bad "unauthenticated mutation ACCEPTED: $SEC" ;;
esac
case "$SEC" in
  *NOCSRF_REFUSED*) ok "session cookie without CSRF header refused" ;;
  *)                bad "cookie-only mutation ACCEPTED (CSRF bypass): $SEC" ;;
esac

# ------------------------------------------------- 4. SRT ingest and routing
step "4. SRT ingest, fan-out and per-destination routing"
D=$(drive dests)
case "$D" in *DESTS_OK*) ok "three differently-routed destinations created" ;;
             *)          bad "destination creation failed: $D" ;; esac

publish "srt://$CTR:6000?mode=caller&latency=200000&transtype=live" 30
sleep 14
N=$(drive tracks)
[ "$N" = "3" ] && ok "SRT ingest probed 3 audio tracks" || bad "SRT ingest probed '$N' tracks, expected 3"
sleep 20   # let the publisher finish so the files are complete
drive stopall >/dev/null
sleep 12

A3=$(rms destA.mkv 300 100);  A12=$(rms destA.mkv 1200 200); A50=$(rms destA.mkv 5000 400)
B3=$(rms destB.mkv 300 100);  B12=$(rms destB.mkv 1200 200)
C3=$(rms destC.mkv 300 100);  C12=$(rms destC.mkv 1200 200); C50=$(rms destC.mkv 5000 400)

if [ -n "$A3" ] && [ -n "$C3" ]; then
  printf "        measured dBFS   destA 300/1200/5000: %s %s %s\n" "$A3" "$A12" "$A50"
  printf "                        destC 300/1200/5000: %s %s %s\n" "$C3" "$C12" "$C50"

  # Track 1 only: its own tone loud, the others down in the filter skirt.
  if louder_than "$A3" "$(awk -v x="$A12" 'BEGIN{print x+15}')"; then
    ok "destination A carries track 1 and not track 2"
  else bad "destination A leaked track 2 (300Hz $A3 vs 1200Hz $A12)"; fi
  if louder_than "$A3" "$(awk -v x="$A50" 'BEGIN{print x+15}')"; then
    ok "destination A carries track 1 and not track 3"
  else bad "destination A leaked track 3 (300Hz $A3 vs 5000Hz $A50)"; fi
  if louder_than "$B12" "$(awk -v x="$B3" 'BEGIN{print x+15}')"; then
    ok "destination B carries track 2 and not track 1"
  else bad "destination B leaked track 1 (1200Hz $B12 vs 300Hz $B3)"; fi

  # All three present in C.
  if louder_than "$C50" "-40"; then ok "destination C carries all three tracks"
  else bad "destination C is missing track 3 (5000Hz $C50)"; fi

  # The amix=normalize=0 guarantee. FFmpeg's default divides by the input
  # count, which would drop a 3-track mix by ~9.5 dB. Mixed must equal solo.
  DELTA=$(awk -v a="$A3" -v c="$C3" 'BEGIN{d=a-c; if(d<0)d=-d; printf "%.2f", d}')
  if awk -v d="$DELTA" 'BEGIN{exit !(d < 3)}'; then
    ok "amix normalize=0 holds — mixed level == solo level (delta ${DELTA} dB)"
  else
    bad "mix is attenuated by ${DELTA} dB — amix is normalising"
  fi
else
  bad "could not measure destination output (no recordings produced)"
fi

# Video must be copied, never re-encoded. Two destinations that received
# completely different AUDIO must contain byte-identical VIDEO frames.
FRAMES=$(inctr 'cd /data/recordings &&
  ffmpeg -v error -i destA.mkv -map 0:v -f framemd5 -c copy /tmp/a.md5 &&
  ffmpeg -v error -i destB.mkv -map 0:v -f framemd5 -c copy /tmp/b.md5 &&
  comm -12 <(grep -v "^#" /tmp/a.md5 | awk "{print \$6}" | sort) \
           <(grep -v "^#" /tmp/b.md5 | awk "{print \$6}" | sort) | grep -c .')
TOTAL=$(inctr 'grep -v "^#" /tmp/a.md5 | grep -c .')
if [ -n "$FRAMES" ] && [ "$FRAMES" -gt 0 ] 2>/dev/null && [ "$FRAMES" = "$TOTAL" ]; then
  ok "video passed through untouched ($FRAMES/$TOTAL frames byte-identical across destinations)"
elif [ -n "$FRAMES" ] && [ "$FRAMES" -gt 0 ] 2>/dev/null; then
  bad "video differs across destinations ($FRAMES/$TOTAL identical) — something re-encoded"
else
  bad "could not compare video frames"
fi

# ------------------------------------------------------- 5. RTMP ingest path
step "5. RTMP ingest (fallback path)"
docker rm -f pub-acc >/dev/null 2>&1
M=$(drive mode rtmp)
case "$M" in *MODE_RTMP*) ok "ingest switched to RTMP" ;; *) bad "could not switch to RTMP: $M" ;; esac
sleep 5
publish_rtmp "rtmp://$CTR:1935/live/stream" 25
sleep 16
NR=$(drive tracks)
if [ -n "$NR" ] && [ "$NR" -ge 1 ] 2>/dev/null; then
  ok "RTMP ingest probed $NR audio track(s)"
else
  bad "RTMP ingest probed '$NR' tracks, expected at least 1"
fi
docker rm -f pub-acc >/dev/null 2>&1
drive mode srt >/dev/null

# --------------------------------------------------------- 6. persistence
step "6. Persistence across a container replacement"
BEFORE=$(drive count)
docker rm -f "$CTR" >/dev/null 2>&1
docker run -d --name "$CTR" --network "$NET" \
  -p "$PORT:8080" -p "$SRTPORT:6000/udp" -p "$RTMPPORT:1935" \
  -v "$VOL:/data" "$IMAGE" >/dev/null 2>&1
sleep 12
AFTER=$(drive count)
if [ "$BEFORE" = "$AFTER" ] && [ "$AFTER" = "3" ]; then
  ok "destinations survive the container being destroyed and recreated ($AFTER on the volume)"
else
  bad "destinations did not survive: $BEFORE before, $AFTER after"
fi

# ---------------------------------------------------------- 7. shutdown
step "7. Graceful shutdown"
drive startall >/dev/null
publish "srt://$CTR:6000?mode=caller&latency=200000&transtype=live" 20 >/dev/null 2>&1
sleep 26   # let the feed go quiet: that is when children are slowest to exit
LIVE=$(inctr 'ps -o pid,args | grep ffmpeg | grep -v grep | wc -l' | tr -d ' ')
S=$(date +%s); docker stop --timeout 30 "$CTR" >/dev/null 2>&1; E=$(date +%s)
CODE=$(docker inspect "$CTR" --format '{{.State.ExitCode}}' 2>/dev/null)
if [ "$CODE" = "0" ]; then
  ok "graceful stop with $LIVE ffmpeg children live: $((E-S))s, exit 0"
else
  bad "stop exited $CODE (137 = SIGKILL) after $((E-S))s with $LIVE children"
fi
# The margin that matters: compose declares stop_grace_period, and if the
# measured shutdown ever approaches it, recordings start getting truncated.
GRACE=$(grep -E "^\s+stop_grace_period:" "$ROOT/docker-compose.yml" | head -1 | awk '{print $2}')
if [ -n "$GRACE" ]; then
  ok "compose declares stop_grace_period: $GRACE (shutdown measured $((E-S))s)"
else
  bad "compose declares no stop_grace_period — Docker's 10s default is too tight"
fi

# ------------------------------------------------------- 8. other arch
step "8. Second architecture"
if docker buildx build --platform linux/amd64 -t "$IMAGE-amd64" "$ROOT" >/tmp/poly-acc-amd64.log 2>&1; then
  ok "image also builds for linux/amd64"
else
  sk "linux/amd64 build unavailable on this host (see /tmp/poly-acc-amd64.log)"
fi

printf "\n\033[1m%d passed, %d failed, %d skipped\033[0m\n" "$pass" "$fail" "$skip"
[ "$fail" -eq 0 ]
