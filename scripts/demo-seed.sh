#!/usr/bin/env bash
#
# Stand up a believable polyemesis installation locally, and photograph it.
#
#   ./scripts/demo-seed.sh                 seed, capture, leave it running
#   ./scripts/demo-seed.sh --no-capture    seed only, then hand it over
#   ./scripts/demo-seed.sh --out DIR       write the screenshots somewhere else
#   ./scripts/demo-seed.sh --down          remove everything this created
#
# WHY THIS EXISTS. capture.spec.ts drives a real browser against a real server
# and waits for real state before every shot, which is the right design and is
# worth nothing pointed at an empty install. Every install available to point it
# at IS empty -- the OVH box has one source, no destinations and no recordings --
# so the committed screenshots are of a product with nothing in it. This builds
# the thing worth photographing: three programmes, a rendition ladder,
# destinations across every platform the product supports, live multitrack
# audio, and a recording library.
#
# WHAT IT WILL NOT DO.
#
#   NOTHING REACHES A REAL PLATFORM. Every destination URL in the fixture is
#   under .invalid, which RFC 2606 reserves so it can never be delegated, and
#   the platform destinations are created DISABLED. A demo seed that publishes
#   to a real ingest is a broadcast nobody meant to make.
#
#   NOTHING RUNS AGAINST YOUR DATA. The server this starts uses a data
#   directory THIS SCRIPT CREATES, under $TMPDIR, and refuses to reuse one it
#   finds already there without --reset. That is the answer to "run by someone
#   on a machine that may already have data": there is nothing to collide with,
#   because none of it is where anything else lives. The seeder carries a
#   second, independent refusal for the --base case -- see refuseIfOccupied in
#   demo_seed_driver.go.
#
#   IT LEAVES NOTHING BEHIND. --down removes the containers, the network and
#   the data directory. That is the whole of what this creates.
#
# Nothing here runs in CI. It builds an image, renders half a gigabyte of media
# and takes several minutes.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURE="$ROOT/scripts/demo-seed.fixture.json"
DRIVER="$ROOT/scripts/demo_seed_driver.go"

PORT="${DEMO_PORT:-8098}"
SRT_PORT="${DEMO_SRT_PORT:-6000}"
WORK="${DEMO_WORK:-${TMPDIR:-/tmp}/polyemesis-demo-seed}"
DATA="$WORK/data"
STAGE="$DATA/.demo-seed"
# docs/media is where capture.spec.ts has always written and where
# docs/media/README.md says the shots live. Overridable because the verification
# run for a change to this script should not overwrite committed artefacts to
# prove it worked.
OUT="${SHOT_DIR:-$ROOT/docs/media}"

IMAGE="${IMAGE:-polyemesis-demo-seed:local}"
NET="polyemesis-demo-net"
SRV="polyemesis-demo-server"
PUB_PREFIX="polyemesis-demo-pub"

CAPTURE=yes
RESET=no
DOWN=no
BASE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --no-capture) CAPTURE=no; shift ;;
    --reset) RESET=yes; shift ;;
    --down) DOWN=yes; shift ;;
    --base) BASE="$2"; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

# Everything this script starts is a container or a directory it made, so
# teardown is exactly those two things. No pkill: there are no host processes to
# kill, and the ones the previous generation of this script tried to kill had
# stopped existing when it moved to containers.
teardown() {
  # Named one at a time rather than piped into xargs: BSD xargs has no -r, so
  # an empty list would run `docker rm -f` with no arguments and print an error
  # on the ordinary path where there is nothing to remove.
  local name
  while read -r name; do
    [ -n "$name" ] && docker rm -f "$name" >/dev/null 2>&1
  done < <(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -E "^${PUB_PREFIX}-|^${SRV}\$" || true)
  docker network rm "$NET" >/dev/null 2>&1 || true
}

if [ "$DOWN" = yes ]; then
  echo "==> removing the demo install"
  teardown
  command rm -rf "$WORK"
  echo "    containers, network and $WORK are gone"
  exit 0
fi

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running" >&2; exit 1; }
command -v go >/dev/null || { echo "go is required (it runs the seeder)" >&2; exit 1; }

# THE FIXTURE IS SCANNED BEFORE IT IS USED, not after it is committed.
#
# It carries stream keys, and this repository already has a published advisory
# about stream keys escaping. The keys in it are deliberately unconvincing, and
# "deliberately" is a claim that has to be checked by the thing that will fail
# CI rather than by the person who wrote them. Skipped, loudly, when gitleaks is
# not installed: a missing local tool should cost a warning, not the run.
if command -v gitleaks >/dev/null 2>&1; then
  if gitleaks detect --no-git --source "$FIXTURE" -c "$ROOT/.gitleaks.toml" >/dev/null 2>&1; then
    echo "==> fixture is clean under gitleaks"
  else
    echo "the demo fixture trips gitleaks. Its fake keys must not resemble real" >&2
    echo "ones -- make them more obviously fake, do not allowlist them." >&2
    gitleaks detect --no-git --source "$FIXTURE" -c "$ROOT/.gitleaks.toml" -v 2>&1 | tail -20 >&2
    exit 1
  fi
else
  echo "==> gitleaks not installed; fixture not scanned locally (CI still will)"
fi

# One password for the seeder and for Playwright's auth setup, generated per
# run. They each used to carry their own literal, and when the two disagreed the
# seeder created the account and the browser then failed on a missing <nav> --
# a symptom several steps from the cause. A literal is also a password committed
# to a public repository, whatever it guards.
if [ -z "${E2E_PASSWORD:-}" ]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is needed to generate E2E_PASSWORD; export one yourself" >&2
    exit 1
  }
  E2E_PASSWORD="Demo-$(openssl rand -hex 16)"
fi
export E2E_PASSWORD

if [ -n "$BASE" ]; then
  # Seeding somebody else's server. The data-directory guarantee above does not
  # apply, so the ONLY protection is the driver's own refusal -- which is why it
  # exists there rather than here.
  echo "==> seeding an install this script did not start: $BASE"
  go run "$DRIVER" "$BASE" setup "$FIXTURE"
  echo "    no publishers started; this mode configures, it does not stream."
  exit 0
fi

# REFUSE A DATA DIRECTORY THIS RUN DID NOT CREATE.
#
# Re-running is normal -- adjusting a shot is something you do repeatedly -- but
# "re-run" and "run over whatever is in there" are different requests. A
# directory left by a previous run holds an admin account whose password was
# generated for THAT run, so silently reusing it fails at sign-in with
# "incorrect username or password", which reads as a bug in the seeder.
if [ -d "$DATA" ] && [ "$RESET" != yes ]; then
  echo "$DATA already exists." >&2
  echo "Re-run with --reset to discard it, or --down to remove everything." >&2
  exit 1
fi

trap 'echo; echo "interrupted; run ./scripts/demo-seed.sh --down to clean up"' INT TERM

# ABSOLUTE, always. capture.spec.ts resolves SHOT_DIR against ui/ and
# capture.config.ts resolves it against ui/e2e/, so a relative --out would put
# the stills and the tour video in two different directories -- and both would
# be somewhere the caller did not name.
case "$OUT" in
  /*) ;;
  *) OUT="$PWD/$OUT" ;;
esac

echo "==> fresh data directory: $DATA"
teardown
command rm -rf "$WORK"
mkdir -p "$DATA/recordings" "$STAGE" "$OUT"

if lsof -nP -iTCP:"$PORT" 2>/dev/null | grep -q LISTEN; then
  echo "port $PORT is already in use:" >&2
  lsof -nP -iTCP:"$PORT" | grep LISTEN >&2
  echo "stop it first, or set DEMO_PORT." >&2
  exit 1
fi

# BUILT EVERY RUN, never reused if present. capture-media.sh learned this the
# expensive way: the image on the machine was a week old, so every screenshot
# committed from it photographed week-old code. A tool that advertises the
# product must photograph the product as it is now.
echo "==> building the UI and the demo image"
( cd "$ROOT" && make build >/dev/null ) || { echo "make build failed" >&2; exit 1; }
GOARCH_LOCAL="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_LOCAL" \
    go build -o "$STAGE/polyemesis-linux" ./cmd/polyemesis ) \
  || { echo "cross-compile for the container failed" >&2; exit 1; }
docker build -q -t "$IMAGE" -f - "$STAGE" >/dev/null <<'DOCKERFILE' || { echo "docker build failed" >&2; exit 1; }
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates
COPY polyemesis-linux /usr/local/bin/polyemesis
ENTRYPOINT ["/usr/local/bin/polyemesis"]
DOCKERFILE

# Server AND publishers in containers on one network, dialing each other by
# name. The host-to-container route does not work here and the reason is
# recorded in capture-media.sh: the publisher reached 127.0.0.1:6000 over IPv4
# while the gosrt listener was bound IPv6-only, and nothing logged a refusal
# because nothing arrived.
echo "==> starting polyemesis on :$PORT"
docker network create "$NET" >/dev/null 2>&1 || true
docker run -d --rm --name "$SRV" --network "$NET" \
  -p "127.0.0.1:$PORT:$PORT" \
  -v "$DATA:/data" \
  "$IMAGE" -addr ":$PORT" -data /data -log warn >/dev/null

BASE_URL="http://127.0.0.1:$PORT"

# ---------------------------------------------------------------- recordings
#
# Rendered BEFORE the seeder runs, so the server's 30-second scan has already
# indexed them by the time anything is photographed. The scanner is what gives
# the library page its durations, sizes and track counts: it ffprobes each
# finished segment once, which is why these are real files at a real bitrate
# rather than empty ones with plausible names.
#
# One short clip is encoded, then concatenated with -c copy to each target
# length. Encoding forty minutes of video would cost minutes of CPU to produce
# exactly the same numbers on the page.
echo "==> rendering the recording library"
docker run --rm -v "$DATA:/data" --entrypoint ffmpeg "$IMAGE" \
  -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=180:sample_rate=48000" \
  -f lavfi -i "sine=frequency=430:sample_rate=48000" \
  -f lavfi -i "sine=frequency=980:sample_rate=48000" \
  -f lavfi -i "sine=frequency=2400:sample_rate=48000" \
  -map 0:v -map 1:a -map 2:a -map 3:a -map 4:a \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -b:v 1200k -g 60 \
  -c:a aac -b:a 96k -t 20 \
  /data/.demo-seed/base.mkv

while IFS=$'\t' read -r NAME SECONDS_LONG; do
  [ -n "$NAME" ] || continue
  LOOPS=$(( (SECONDS_LONG + 19) / 20 ))
  : > "$STAGE/concat.txt"
  for _ in $(seq 1 "$LOOPS"); do echo "file '/data/.demo-seed/base.mkv'" >> "$STAGE/concat.txt"; done
  # -map 0 IS LOAD-BEARING. Without it FFmpeg's default stream selection keeps
  # one video and ONE audio stream, so a four-track master concatenates down to
  # a single track -- and the recordings page, whose whole subtitle is "every
  # ingest audio track preserved", reports TRACKS 1 for every segment. The
  # numbers on that page are ffprobe measurements, so this is not cosmetic: the
  # page would be telling the truth about a file the seed got wrong.
  docker run --rm -v "$DATA:/data" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -y \
    -f concat -safe 0 -i /data/.demo-seed/concat.txt \
    -map 0 -c copy -t "$SECONDS_LONG" "/data/recordings/$NAME"
  printf '    %s  %d:%02d\n' "$NAME" $((SECONDS_LONG / 60)) $((SECONDS_LONG % 60))
done < <(go run "$DRIVER" "$BASE_URL" recordings "$FIXTURE")

# ------------------------------------------------------------------- seeding

echo "==> creating the programmes"
PLAN="$STAGE/plan.tsv"
go run "$DRIVER" "$BASE_URL" setup "$FIXTURE" > "$PLAN"
[ -s "$PLAN" ] || { echo "the seeder created no programmes" >&2; exit 1; }

# One publisher per programme, all on the SHARED SRT listener, told apart only
# by the token in the streamid -- which is how the product itself addresses
# programmes and is what acceptance-multisource.sh proves.
#
# Through the REAL ingest rather than injected into the relay hub, and that is
# not fussiness: injection leaves the ingest with no publisher, so the dashboard
# reads "Ingest Offline", the bitrate is a dash and every track in the routing
# editor says "no signal". The routing is right either way; only one of them
# photographs as a working product.
echo "==> publishing into each programme"
N=0
while IFS=$'\t' read -r SID TOKEN VLAVFI VKBPS ALAVFI; do
  [ -n "$SID" ] || continue
  N=$((N + 1))
  ARGS=(-hide_banner -loglevel error -re -f lavfi -i "$VLAVFI")
  MAPS=(-map 0:v)
  IDX=1
  IFS='|' read -r -a TRACKS <<< "$ALAVFI"
  for A in "${TRACKS[@]}"; do
    ARGS+=(-f lavfi -i "$A")
    MAPS+=(-map "$IDX:a")
    IDX=$((IDX + 1))
  done
  docker run -d --rm --name "$PUB_PREFIX-$SID" --network "$NET" --entrypoint ffmpeg \
    "$IMAGE" "${ARGS[@]}" "${MAPS[@]}" \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v "${VKBPS}k" \
    -c:a aac -b:a 128k -ac 2 \
    -f mpegts \
    "srt://$SRV:$SRT_PORT?streamid=$TOKEN&mode=caller&transtype=live&latency=200000" >/dev/null
  echo "    programme $SID publishing (${#TRACKS[@]} audio tracks)"
done < "$PLAN"

echo "==> waiting for every programme to go live and probe"
go run "$DRIVER" "$BASE_URL" wait "$FIXTURE"

echo "==> creating the rendition ladder and the destinations"
go run "$DRIVER" "$BASE_URL" finish "$FIXTURE"

# The ladder has to actually start before it is worth photographing: an encoder
# that has not opened yet reports no consumers and no bitrate, and the ladder
# view is the one page where those two numbers are the whole point.
echo "==> letting the encoders and file destinations settle"
sleep 25

# REFUSE TO PHOTOGRAPH AN UNDER-SEEDED INSTALL.
#
# The previous generation of this class of script only warned, and a run where
# the publisher had already died still reported everything passed and wrote a
# full set of screenshots claiming the product does not work. Each count below
# has its own silent zero -- a destination refused for a profile the probe did
# not support, a recordings directory the container could not write -- and none
# of them fails the capture on its own.
echo "==> checking what actually got seeded"
SHORT=0
while IFS=$'\t' read -r WHAT GOT WANT; do
  printf '    %-14s %s/%s\n' "$WHAT" "$GOT" "$WANT"
  [ "$GOT" -ge "$WANT" ] || SHORT=1
done < <(go run "$DRIVER" "$BASE_URL" census "$FIXTURE")
if [ "$SHORT" = 1 ]; then
  echo "the install is short of what the fixture describes; refusing to capture." >&2
  echo "--- server ---" >&2; docker logs "$SRV" 2>&1 | tail -25 >&2
  exit 1
fi

if [ "$CAPTURE" = no ]; then
  echo
  echo "==> seeded, streaming, and left running at $BASE_URL"
  echo "    sign in as admin / \$E2E_PASSWORD ($E2E_PASSWORD)"
  echo "    ./scripts/demo-seed.sh --down  removes all of it"
  exit 0
fi

echo "==> capturing into $OUT"
( cd "$ROOT/ui" && BASE_URL="$BASE_URL" SHOT_DIR="$OUT" \
    npx --no-install playwright test --config=e2e/capture.config.ts )

echo
echo "==> done"
ls -la "$OUT"/*.png
echo
echo "the install is still up at $BASE_URL (admin / $E2E_PASSWORD)"
echo "./scripts/demo-seed.sh --down  removes the containers and $WORK"
