#!/usr/bin/env bash
#
# Photograph the dashboard at ONE, TWO and THREE programmes.
#
#   ./scripts/capture-lanes.sh
#
# Output goes to docs/media/ as:
#
#   dashboard-1-programme.png    no lanes, one full-width preview, flat grid
#   dashboard-2-programmes.png   lanes appear; each carries its own preview
#   dashboard-3-programmes.png   the same shape, one lane further
#
# WHY THIS EXISTS SEPARATELY FROM capture-media.sh. That script seeds a fixed
# two-programme install and photographs eighteen pages of it. This one seeds a
# single install THREE TIMES OVER, adding a programme between shots, because
# the thing being demonstrated is a threshold rather than a page: Dashboard.tsx
# draws nothing per-programme with one source and everything per-programme with
# two, and no single install can show both halves of that.
#
# ONE INSTALL, NOT THREE. Standing up a fresh server per shot would change
# every timestamp, port and generated id on the page, so the three images would
# differ everywhere except in the thing a reader is being asked to compare.
# Seeding three and deleting backwards is worse still: a deleted programme
# leaves its destinations behind as orphans, which the dashboard draws in a
# flagged group by design, so the one-programme shot would carry a paragraph
# about destinations whose programme is missing -- a fault state photographed
# as if it were the ordinary single-programme dashboard.
#
# Nothing here runs in CI. It writes committed artefacts and takes minutes.

set -euo pipefail

PORT="${PORT:-8098}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${TMPDIR:-/tmp}/polyemesis-lanes"
OUT="${SHOT_DIR:-$ROOT/docs/media}"
IMAGE="${IMAGE:-polyemesis-capture:local}"
NET="polyemesis-lanes-net"
SRV_NAME="polyemesis-lanes-server"
# One publisher container per programme. Separate containers rather than one
# ffmpeg with three outputs: a single process that dies takes every programme
# with it, and the shot would then show three lanes all offline with no way to
# tell which stream actually failed.
SRC_PREFIX="polyemesis-lanes-source"

# A DIFFERENT PORT AND NETWORK FROM capture-media.sh, deliberately. The two
# scripts are run back to back while adjusting shots, and sharing either would
# mean one silently photographing the other's install -- the same "refuse to
# run against a server this script did not start" failure capture-media.sh
# already carries a guard for.
SRT_PORT="${SRT_PORT:-6000}"

cleanup() {
  for n in "$SRC_PREFIX-1" "$SRC_PREFIX-2" "$SRC_PREFIX-3" "$SRV_NAME"; do
    docker rm -f "$n" >/dev/null 2>&1 || true
  done
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

command -v docker >/dev/null || { echo "docker required"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running"; exit 1; }

if [ -z "${E2E_PASSWORD:-}" ]; then
  command -v openssl >/dev/null || {
    echo "openssl is needed to generate E2E_PASSWORD" >&2; exit 1; }
  E2E_PASSWORD="E2E-$(openssl rand -hex 16)"
fi
export E2E_PASSWORD

echo "==> fresh data directory"
rm -rf "$WORK"; mkdir -p "$WORK" "$OUT"

# Refuse to run against a server this script did not start; otherwise the
# seeder talks to a leftover instance with a different admin password and
# reports "incorrect username or password", which reads like a bug in the
# seeder rather than a stale process.
if lsof -nP -iTCP:"$PORT" 2>/dev/null | grep -q LISTEN; then
  echo "port $PORT is already in use:"
  lsof -nP -iTCP:"$PORT" | grep LISTEN
  echo "stop it first (a previous run may have been interrupted)."
  exit 1
fi

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

# publish <container-suffix> <token> <video-filter> <freq>...
#
# Each programme gets a VISIBLY DIFFERENT picture and its own tones. Three
# lanes showing the identical frame look like one lane rendered three times,
# which is the reading these shots exist to prevent.
#
# THE TRACK COUNT IS PER PROGRAMME AND HAS TO MATCH ITS DESTINATIONS. Every
# frequency after the video filter becomes one mono audio track. The first run
# of this script published two tracks for a programme whose destinations select
# three, and the dashboard said so on two cards at once -- "track 3 is selected
# but not present on the ingest; it is ignored". That warning is the product
# working correctly, and it is exactly the wrong thing to photograph in a shot
# whose subject is how lanes are laid out: a reader cannot tell a deliberate
# demonstration from a broken install.
publish() {
  local n="$1" token="$2" vf="$3"
  shift 3
  local args=(-hide_banner -loglevel error -re -f lavfi -i "$vf")
  local maps=(-map 0:v)
  local i=1
  for f in "$@"; do
    args+=(-f lavfi -i "sine=frequency=$f:sample_rate=48000")
    maps+=(-map "$i:a")
    i=$((i + 1))
  done
  docker run -d --rm --name "$SRC_PREFIX-$n" --network "$NET" --entrypoint ffmpeg \
    "$IMAGE" "${args[@]}" "${maps[@]}" \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -b:v 2000k \
    -c:a aac -b:a 128k \
    -metadata "comment=lanes-source-$n" \
    -map 0 -f mpegts -flush_packets 1 \
    "srt://${SRV_NAME}:${SRT_PORT}?streamid=${token}&mode=caller&transtype=live&latency=200000" >/dev/null
}

# shoot <n> <filename>
#
# REFUSES rather than warns. lanes.spec.ts asserts the programme count from
# both directions, so a run where a programme failed to seed or a stream failed
# to arrive fails here instead of writing an image filed under a count it does
# not show.
shoot() {
  local n="$1" name="$2"
  echo "==> confirming $n programme(s) live"
  ( cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT" waitlive ) || {
    echo "the ingest never went live at $n programme(s) -- refusing to capture."
    docker logs "$SRV_NAME" 2>&1 | tail -15 || true
    exit 1
  }
  echo "==> capturing $name"
  ( cd "$ROOT/ui" && BASE_URL="http://127.0.0.1:$PORT" SHOT_DIR="$OUT" \
      LANES_N="$n" SHOT="$name" \
      npx --no-install playwright test --config=e2e/lanes.config.ts )
}

# ---------------------------------------------------------------- one
#
# `solo` stops the seeder at one programme. Everything else it does -- track
# labels, destinations, the loudness monitor, preview on -- still runs, because
# a one-programme dashboard with no destinations photographs as an install that
# does not work rather than as the shape below the threshold.
echo "==> seeding one programme"
SEEDED="$(cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT" solo)"
TOKEN1="$(printf '%s\n' "$SEEDED" | sed -n 2p)"
[ -n "$TOKEN1" ] || { echo "no publish token for the first programme"; exit 1; }

echo "==> publishing programme 1"
publish 1 "$TOKEN1" "testsrc2=size=1280x720:rate=30" 300 900 2000
sleep 15
shoot 1 "dashboard-1-programme.png"

# ---------------------------------------------------------------- two
echo "==> adding the second programme"
TOKEN2="$(cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT" add b | sed -n 1p)"
[ -n "$TOKEN2" ] || { echo "no publish token for the second programme"; exit 1; }
echo "==> publishing programme 2, same port, different streamid"
publish 2 "$TOKEN2" "smptebars=size=1280x720:rate=30" 440 1200
sleep 15
shoot 2 "dashboard-2-programmes.png"

# ---------------------------------------------------------------- three
echo "==> adding the third programme"
TOKEN3="$(cd "$WORK" && go run "$ROOT/scripts/seed_demo.go" "$PORT" add c | sed -n 1p)"
[ -n "$TOKEN3" ] || { echo "no publish token for the third programme"; exit 1; }
echo "==> publishing programme 3, same port, different streamid"
publish 3 "$TOKEN3" "testsrc=size=1280x720:rate=30" 660 1800
sleep 15
shoot 3 "dashboard-3-programmes.png"

rm -rf "$OUT/.playwright-lanes"
echo
echo "wrote:"
for f in dashboard-1-programme.png dashboard-2-programmes.png dashboard-3-programmes.png; do
  echo "  $OUT/$f"
done
