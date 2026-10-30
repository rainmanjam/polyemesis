#!/usr/bin/env bash
# Degraded-link failover acceptance (#6): a real SRT encoder whose uplink is
# throttled, then made lossy, is taken off air while it is STILL CONNECTED,
# and comes back once the uplink recovers.
#
# Everything below the selector's decision is real: polyemesis in its own
# container image, an FFmpeg encoder publishing SRT from a second container,
# and Linux tc shaping that second container's egress -- a token bucket for
# "the cellular link fell to 400 kbit", netem for "a third of the packets are
# lost". The verdict is read back from /status, the same document the
# dashboard, MQTT and the Stream Deck are built from.
#
# The encoder container needs NET_ADMIN for tc; nothing else here does.
#
# Usage:  ./scripts/acceptance-degraded.sh
# Requires: docker (with a running daemon), go
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
. "$SCRIPTS/lib-preflight.sh"
. "$SCRIPTS/lib-watchdog.sh"
trap 'poly_verdict_trap $?' EXIT
poly_require_docker
poly_require_cmd go "needed to run the acceptance driver via 'go run'"

PREFIX="${POLY_DOCKER_PREFIX:-poly-}"
IMAGE="${PREFIX}polyemesis:degraded"
ENC_IMAGE="${PREFIX}encoder:degraded"
CTR="${PREFIX}deg-server"
ENC="${PREFIX}deg-encoder"
NET="${PREFIX}deg-net"
VOL="${PREFIX}deg-data"
PORT="${POLY_DEG_PORT:-18096}"
DRIVER="$SCRIPTS/acceptance_degraded_driver.go"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }
note() { printf "        %s\n" "$1"; }

cleanup() {
  docker rm -f "$ENC" "$CTR" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  docker volume rm "$VOL" >/dev/null 2>&1
  if [ "${KEEP_IMAGES:-0}" != 1 ]; then
    docker rmi "$IMAGE" "$ENC_IMAGE" >/dev/null 2>&1
  fi
}
trap 'poly_verdict_trap $? cleanup' EXIT

drive() { (cd "$ROOT" && go run "$DRIVER" "http://127.0.0.1:$PORT" "$@"); }
st()    { drive status 2>/dev/null; }

# wait_for <description> <seconds> <bash-predicate-on-$S>
wait_for() {
  local what="$1" secs="$2" pred="$3" _
  for _ in $(seq 1 "$secs"); do
    S="$(st)"
    if eval "$pred"; then return 0; fi
    sleep 1
  done
  note "gave up waiting for: $what; last status: $S"
  return 1
}

# ------------------------------------------------------------------ images
step "1. Build"
if docker build -q -t "$IMAGE" --build-arg VERSION=degraded "$ROOT" >/dev/null 2>"/tmp/${PREFIX}deg-build.log"; then
  ok "polyemesis image built"
else
  bad "polyemesis image did not build (see /tmp/${PREFIX}deg-build.log)"; exit 1
fi
# The encoder: FFmpeg with libsrt, plus iproute2 for tc. Alpine's FFmpeg is
# built with libsrt.
if printf 'FROM alpine:3.20\nRUN apk add --no-cache ffmpeg iproute2 iproute2-tc\n' \
   | docker build -q -t "$ENC_IMAGE" - >/dev/null 2>&1; then
  ok "encoder image built"
else
  bad "encoder image did not build"; exit 1
fi

# ------------------------------------------------------------------ boot
step "2. Boot and configure"
docker network create "$NET" >/dev/null
docker volume create "$VOL" >/dev/null
docker run -d --name "$CTR" -e POLYEMESIS_SETUP_CODE --network "$NET" \
  -p "127.0.0.1:$PORT:8080" -v "$VOL:/data" "$IMAGE" >/dev/null
if drive >/dev/null; then ok "failover with a slate and degraded thresholds configured"; else bad "setup failed"; exit 1; fi
TOKEN="$(drive token)"
if [ -n "$TOKEN" ]; then ok "got the programme's publish token"; else bad "no publish token"; exit 1; fi

# ------------------------------------------------------------------ encoder
step "3. A healthy 2.5 Mbps SRT encoder goes on air"
docker run -d --name "$ENC" --network "$NET" --cap-add NET_ADMIN --entrypoint sh "$ENC_IMAGE" -c \
  "exec ffmpeg -hide_banner -loglevel error -re -f lavfi -i testsrc2=size=1280x720:rate=30 \
     -f lavfi -i sine=frequency=440:sample_rate=48000 \
     -c:v libx264 -preset ultrafast -tune zerolatency -b:v 2500k -maxrate 2500k -bufsize 2500k -g 60 \
     -c:a aac -b:a 128k -f mpegts \
     'srt://$CTR:6000?streamid=$TOKEN&latency=500000&mode=caller'" >/dev/null
if wait_for "the primary on air, healthy" 40 '[[ "$S" == "primary true "* && "$S" == *"||"* || "$S" =~ ^primary\ true\ [0-9]+\|\| ]]'; then
  ok "primary on air and not degraded: $S"
else
  bad "the healthy encoder never went on air"
fi

# ------------------------------------------------------------------ throttle
step "4. The uplink falls to 400 kbit while the encoder stays connected"
docker exec "$ENC" tc qdisc add dev eth0 root tbf rate 400kbit burst 16kbit latency 600ms
if wait_for "the slate on air with a degraded: reason" 30 '[[ "$S" == slate\ true* && "$S" == *"degraded: "* ]]'; then
  ok "slate on air while the encoder is still delivering: $S"
  if [[ "$S" == *"below 1000 kbps"* ]]; then ok "the reason names the bitrate breach"; else bad "reason does not name the bitrate: $S"; fi
else
  bad "a throttled but connected encoder was not taken off air"
fi

step "5. The uplink recovers; the primary comes back after the sustain window"
docker exec "$ENC" tc qdisc del dev eth0 root
if wait_for "the primary back on air, not degraded" 40 '[[ "$S" =~ ^primary\ true\ [0-9]+\|\| ]]'; then
  ok "primary back on air and healthy: $S"
else
  bad "the primary did not come back after the uplink recovered"
fi

# ------------------------------------------------------------------ loss
step "6. A third of the packets are lost (netem)"
docker exec "$ENC" tc qdisc add dev eth0 root netem loss 35%
if wait_for "a degraded switch with the loss named" 40 '[[ "$S" == slate* && "$S" == *"degraded: "* && "$S" == *"loss "* ]]'; then
  ok "lossy uplink taken off air: $S"
else
  bad "a 35% lossy uplink was not judged degraded on loss"
fi
docker exec "$ENC" tc qdisc del dev eth0 root
if wait_for "the primary back after the loss clears" 60 '[[ "$S" =~ ^primary\ true\ [0-9]+\|\| ]]'; then
  ok "primary back after the loss cleared: $S"
else
  bad "the primary did not come back after the loss cleared"
fi

printf "\n%d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ]
