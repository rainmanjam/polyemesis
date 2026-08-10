#!/usr/bin/env bash
#
# Capture screenshots and video of a LIVE polyemesis, for the README and the
# website.
#
#   ./scripts/capture-media.sh
#
# Brings up a real server on a scratch data directory, seeds it with a
# three-track source feeding three destinations that each take a different mix,
# pushes a synthetic stream through it, and photographs the result. Output goes
# to docs/media/.
#
# The synthetic stream is the point. Screenshots of an empty install show a
# product that does nothing, and the one claim polyemesis makes — a different
# audio mix per destination — is only visible when audio is actually flowing.
#
# Nothing here runs in CI. It writes committed artefacts and takes minutes.

set -euo pipefail

PORT=8099
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${TMPDIR:-/tmp}/polyemesis-capture"
OUT="$ROOT/docs/media"
# Server AND publisher both run in Docker, on one network.
#
# The host-to-container route was tried first and does not work here: the
# publisher reached 127.0.0.1:6000 over IPv4 while the gosrt listener was bound
# IPv6-only, and srtserver logged no refusal at all because nothing arrived.
# Container-to-container over a user-defined network sidesteps the question
# entirely -- the publisher dials the server by container name.
#
# The image is built locally from the already-compiled UI rather than pulled:
# rainmanjam/polyemesis:latest does not exist until the first release, and a
# capture tool must not depend on the thing it exists to advertise.
IMAGE="${IMAGE:-polyemesis-capture:local}"
NET="polyemesis-capture-net"
SRV_NAME="polyemesis-capture-server"
SRC_NAME="polyemesis-capture-source"

# One password for the seeder and for Playwright's auth setup. They each used
# to carry their own literal, so the seeder created the admin account and the
# browser then tried a password nobody had set -- surfacing as a missing <nav>,
# which points nowhere near the cause. Exported so both read the same value.
#
# Generated per run rather than written down. The account it guards lives for
# one capture, but a literal in a public repository is a committed password
# whatever it guards, so the seeder and auth.setup.ts both now REFUSE to start
# without this variable.
#
# openssl is checked rather than assumed. An empty command substitution would
# leave a four-character password, fail the eight-character floor, and fail at
# sign-in -- the same shape of several-steps-removed symptom described above.
if [ -z "${E2E_PASSWORD:-}" ]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is needed to generate E2E_PASSWORD;" >&2
    echo "export one yourself instead" >&2
    exit 1
  }
  E2E_PASSWORD="E2E-$(openssl rand -hex 16)"
fi
export E2E_PASSWORD

# Everything this script starts is a container, so cleanup is docker's job. The
# host-process kills that used to be here were left over from the binary-launch
# version and had been killing nothing for some time.
cleanup() {
  # FFmpeg children sit in their own process groups, so killing the server
  # does not take them with it -- the same trap the troubleshooting page warns
  # operators about, and it strands the ingest port for the next run.
  pkill -f "capture-source" 2>/dev/null || true
  docker rm -f "$SRC_NAME" "$SRV_NAME" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

command -v docker >/dev/null || { echo "docker required"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running"; exit 1; }

echo "==> fresh data directory"
rm -rf "$WORK"; mkdir -p "$WORK" "$OUT"

# Refuse to run against a server this script did not start. Without this the
# seeder happily talks to a leftover instance -- different data directory,
# different admin password -- and reports "incorrect username or password",
# which reads like a bug in the seeder rather than a stale process.
if lsof -nP -iTCP:"$PORT" 2>/dev/null | grep -q LISTEN; then
  echo "port $PORT is already in use:"
  lsof -nP -iTCP:"$PORT" | grep LISTEN
  echo "stop it first (a previous capture run may have been interrupted)."
  exit 1
fi

# BUILD THE IMAGE, every run.
#
# This used to check only that the image EXISTED, and reused whatever was there.
# The one on this machine turned out to be a week old, so every capture -- and
# every screenshot committed from one -- photographed week-old code. It is the
# same defect acceptance-failover.sh had, where a stale binary in the repo root
# made a local run pass against a program nobody had built.
#
# A capture tool that advertises the product must photograph the product as it
# is now, or the screenshots are documentation of something else. Rebuilding
# costs a minute; being wrong about what shipped costs more.
#
# --no-cache is deliberately NOT passed: the layer cache is keyed on the binary
# and the built UI, both of which are regenerated above, so a cached layer here
# means nothing changed.
echo "==> building the UI and the capture image"
( cd "$ROOT" && make build >/dev/null ) || { echo "make build failed"; exit 1; }
GOARCH_LOCAL="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_LOCAL" \
    go build -o "$WORK/polyemesis-linux" ./cmd/polyemesis ) || {
  echo "cross-compile for the container failed"; exit 1; }
docker build -q -t "$IMAGE" -f - "$WORK" >/dev/null <<'DOCKERFILE' || { echo "docker build failed"; exit 1; }
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates
COPY polyemesis-linux /usr/local/bin/polyemesis
ENTRYPOINT ["/usr/local/bin/polyemesis"]
DOCKERFILE

echo "==> starting polyemesis (container) on :$PORT"
docker network create "$NET" >/dev/null 2>&1 || true
docker run -d --rm --name "$SRV_NAME" --network "$NET" \
  -p "127.0.0.1:$PORT:$PORT" \
  "$IMAGE" -addr ":$PORT" -data /data -log warn >/dev/null

echo "==> seeding"
SEEDED="$(cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT")"
RELAY="$(printf '%s\n' "$SEEDED" | sed -n 1p)"
TOKEN="$(printf '%s\n' "$SEEDED" | sed -n 2p)"
# A relay port is a number. Anything else means the seeder failed partway and
# printed a diagnostic, which would otherwise be handed to ffmpeg as a port.
case "$RELAY" in
  ''|*[!0-9]*) echo "seed failed (relay='$RELAY'); server log:"; tail -20 "$WORK/server.log"; exit 1 ;;
esac
echo "    relay hub on udp/$RELAY"

# Three distinct tones, one per track, so the meters differ visibly from each
# other in a still image.
#
# Pushed through the REAL SRT ingest when possible, and that is not fussiness.
# Injecting into the relay hub -- which is what smoketest.go does, correctly,
# because it only cares whether routing works -- leaves the ingest itself with
# no publisher: the dashboard reads "Ingest Offline", the bitrate is a dash and
# every track in the routing editor says "no signal". The routing is genuinely
# right in both cases, but only one of them photographs as a working product.
#
# The publisher runs in Docker because the SRT hop needs an ffmpeg with libsrt
# and Homebrew's has none. polyemesis itself needs no such thing: its listener
# is pure-Go gosrt and delivers straight to the relay, with FFmpeg nowhere in
# the receive path.
ENC_ARGS=(-hide_banner -loglevel error -re
  -f lavfi -i "testsrc2=size=1280x720:rate=30"
  -f lavfi -i "sine=frequency=300:sample_rate=48000"
  -f lavfi -i "sine=frequency=900:sample_rate=48000"
  -f lavfi -i "sine=frequency=2000:sample_rate=48000"
  -map 0:v -map 1:a -map 2:a -map 3:a
  -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -b:v 2500k
  -c:a aac -b:a 128k
  -metadata comment=capture-source
  -map 0 -f mpegts -flush_packets 1)

# The shared SRT listener port. 6000 is the built-in default; override if the
# seeded install ever moves it.
SRT_PORT="${SRT_PORT:-6000}"
if [ -n "$TOKEN" ] && docker info >/dev/null 2>&1; then
  echo "==> publishing over SRT, container to container"
  # By container name on the shared network. Not host.docker.internal, and not
  # 127.0.0.1 -- inside a container that is the container itself.
  docker run -d --rm --name "$SRC_NAME" --network "$NET" --entrypoint ffmpeg \
    "$IMAGE" "${ENC_ARGS[@]}" \
    "srt://${SRV_NAME}:${SRT_PORT}?streamid=${TOKEN}&mode=caller&transtype=live&latency=200000" >/dev/null
else
  echo "==> no token; falling back to relay injection (the ingest will read offline)"
  docker run -d --rm --name "$SRC_NAME" --network "$NET" --entrypoint ffmpeg \
    "$IMAGE" "${ENC_ARGS[@]}" "udp://${SRV_NAME}:${RELAY}?pkt_size=1316" >/dev/null
fi

echo "==> waiting for the engine to probe and the destinations to start"
sleep 15
# REFUSE to capture an offline ingest.
#
# The previous version only warned, and a run where the publisher had already
# died still reported 8/8 passed and wrote a full set of screenshots -- ones
# that quietly claim the product does not work. That is the same defect this
# repo already names once: an acceptance suite reporting "7 passed, 0 failed --
# PASSED" having silently skipped five checks. A capture that cannot prove the
# stream arrived must fail, not narrate.
# Asked through the seeder, which holds a session. /api/v1/status requires one,
# and an unauthenticated poll gets 401 -- indistinguishable, to a shell grepping
# for '"live":true', from a stream that never arrived. That is exactly how an
# earlier version of this guard declared a perfectly healthy ingest dead.
echo "==> confirming the ingest is live before photographing it"
if (cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT" waitlive); then
  LIVE=yes
else
  LIVE=no
fi
if [ "$LIVE" != yes ]; then
  echo "the ingest never went live -- refusing to capture screenshots of a dead stream."
  echo "--- publisher ---"; docker logs "$SRC_NAME" 2>&1 | tail -15 || true
  echo "--- server ---";    docker logs "$SRV_NAME" 2>&1 | tail -15 || true
  exit 1
fi
echo "    ingest is live"

echo "==> capturing"
( cd "$ROOT/ui" && BASE_URL="http://127.0.0.1:$PORT" \
    npx --no-install playwright test --config=e2e/capture.config.ts )

# Playwright names video files by a hash, which is useless in a README.
echo "==> collecting video"
VID="$(find "$OUT/.playwright" -name '*.webm' -print0 2>/dev/null | xargs -0 ls -S 2>/dev/null | head -1 || true)"
if [ -n "$VID" ]; then
  cp "$VID" "$OUT/tour.webm"
  # An MP4 alongside it, because GitHub will not inline a .webm in a README.
  if ffmpeg -hide_banner -loglevel error -y -i "$VID" \
       -c:v libx264 -pix_fmt yuv420p -movflags +faststart -an "$OUT/tour.mp4" 2>/dev/null; then
    echo "    docs/media/tour.mp4"
  fi
  echo "    docs/media/tour.webm"
fi
rm -rf "$OUT/.playwright"

echo
echo "==> done"
ls -la "$OUT"
