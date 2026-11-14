#!/usr/bin/env bash
#
# Stand up a believable polyemesis installation locally, and photograph it.
#
#   ./scripts/demo-seed.sh                 seed, capture, leave it running
#   ./scripts/demo-seed.sh --no-capture    seed only, then hand it over
#   ./scripts/demo-seed.sh --out DIR       write the screenshots somewhere else
#   ./scripts/demo-seed.sh --down          remove everything this created
#
#   --scene full|sports   which installation (default full). The sports scene is
#                         one match feed with six audio tracks going to five
#                         destinations labelled YouTube that publish only to a
#                         receiver container on an internal Docker network; see
#                         scripts/lib-demo-sports.sh.
#   --state NAME          sports only, acts on the running rig and exits:
#                         routing-initial | routing-final | start | stop |
#                         status | guard | waitlive
#   --proof               sports only: read each destination's path back from
#                         the receiver and check it carries its own language
#   DEMO_PORT (8098 full, 8097 sports)   DEMO_WORK   DEMO_FPS (60)
#   DEMO_FIXTURE   DEMO_REUSE_IMAGE=1 (skip the image rebuild; never set here)
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
#   NOTHING REACHES A REAL PLATFORM. Three layers, each checked by code:
#     1. Before anything starts, internal/demorig refuses the fixture. Sports:
#        every destination URL, and backup URL, must be on the receiver's host
#        (the receiver container's name) and no destination may be enabled.
#        Full: every network destination (rtmp, rtmps, srt) must be on a
#        .invalid host (RFC 2606 reserves it, so it can never be delegated) and
#        must not be enabled, every alert URL must be https on a .invalid host,
#        and every "start" schedule must name file destinations. Both scenes:
#        no platform account, extra FFmpeg argument or multitrack; stream keys
#        of only A-Z a-z 0-9 . _ ~ - (librtmp reads options out of a key); and
#        every key in what is sent to the server spelled exactly as an
#        allow-list has it (the server matches keys ignoring case, so a variant
#        would slip past).
#     2. The sports scene checks the rows the server stored before every start.
#     3. The server runs on an --internal Docker network (asked of Docker, not
#        assumed; engines older than 26.0.0 are refused) with no route off the
#        machine and no published port, reached through a relay bound to
#        127.0.0.1.
#   Network destinations are created disabled. A demo seed that publishes to a
#   real ingest is a broadcast nobody meant to make.
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
FIXTURE="${DEMO_FIXTURE:-$ROOT/scripts/demo-seed.fixture.json}"
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
# Overridable so a second checkout (a worktree, an agent) can run its own demo
# install beside this one without the two tearing down each other's containers:
# teardown removes everything matching these names.
NET="${DEMO_NET:-polyemesis-demo-net}"
SRV="${DEMO_SRV:-polyemesis-demo-server}"
PUB_PREFIX="${DEMO_PUB_PREFIX:-polyemesis-demo-pub}"

# THE VERSION THE UI PRINTS IN ITS HEADER, and the reason the marketing shots
# read "dev". A plain `go build` leaves main.version at its default, so every
# capture showed a development build -- an unfinished-looking label on the
# images whose whole job is to look like the product people install. Default to
# the latest release tag rather than `git describe`'s v0.11.0-12-gabc123: the
# screenshots stand for a release, not for one commit of main.
DEMO_VERSION="${DEMO_VERSION:-$(cd "$ROOT" && git describe --tags --abbrev=0 2>/dev/null || echo dev)}"

CAPTURE=yes
RESET=no
DOWN=no
BASE=""
SCENE=full
STATE_NAME=""
PROOF=no
FPS="${DEMO_FPS:-60}"
# Set by sports_configure; empty for the full scene, so teardown has nothing
# extra to remove there.
RECV=""
PROXY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --no-capture) CAPTURE=no; shift ;;
    --reset) RESET=yes; shift ;;
    --down) DOWN=yes; shift ;;
    --base) BASE="$2"; shift 2 ;;
    --scene) SCENE="${2:-}"; shift 2 ;;
    --state) STATE_NAME="${2:-}"; shift 2 ;;
    --proof) PROOF=yes; shift ;;
    -h|--help) sed -n '2,/^$/p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

# The internal-network and relay functions are shared by both scenes.
. "$ROOT/scripts/lib-demo-sports.sh"
case "$SCENE" in
  full)
    # The full scene runs on an --internal network behind the same relay.
    PROXY="${DEMO_PROXY:-polyemesis-demo-proxy}"
    ;;
  sports)
    sports_configure
    ;;
  *) echo "unknown scene: $SCENE (full or sports)" >&2; exit 2 ;;
esac
if [ -n "$STATE_NAME" ] || [ "$PROOF" = yes ]; then
  [ "$SCENE" = sports ] || { echo "--state and --proof act on the sports rig: add --scene sports" >&2; exit 2; }
fi
case "$STATE_NAME" in
  ""|routing-initial|routing-final|start|stop|status|guard|waitlive) ;;
  *) echo "unknown state: $STATE_NAME" >&2; exit 2 ;;
esac
BASE_URL="http://127.0.0.1:$PORT"

# Everything this script starts is a container or a directory it made, so
# teardown is exactly those two things. No pkill: there are no host processes to
# kill, and the ones the previous generation of this script tried to kill had
# stopped existing when it moved to containers.
teardown() {
  # Named one at a time rather than piped into xargs: BSD xargs has no -r, so
  # an empty list would run `docker rm -f` with no arguments and print an error
  # on the ordinary path where there is nothing to remove.
  #
  # NOTHING IN HERE MAY ABORT THE SCRIPT. Under `set -e` a failed `docker rm` (the
  # daemon is down, the container is already gone) used to end a --down before it
  # reached the work directory, leaving the credential files behind.
  local name
  while read -r name; do
    if [ -n "$name" ]; then docker rm -f "$name" >/dev/null 2>&1 || true; fi
  done < <(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -E "^${PUB_PREFIX}-|^${SRV}\$" || true)
  # The sports rig's receiver and relay, by name: they are not publishers, and a
  # listing that came back empty must not leave them behind.
  for name in "$SRV" "$RECV" "$PROXY"; do
    if [ -n "$name" ]; then docker rm -f "$name" >/dev/null 2>&1 || true; fi
  done
  docker network rm "$NET" >/dev/null 2>&1 || true
}

# Is something listening on 127.0.0.1:$PORT? 0 yes, 1 no, 2 cannot tell. A bash
# /dev/tcp connect, so no extra tool is needed; "cannot tell" is a refusal, never
# a pass (a missing lsof used to read as "nothing listening").
port_in_use() {
  local err
  if err="$( ( exec 3<>"/dev/tcp/127.0.0.1/$PORT" ) 2>&1 )"; then
    return 0
  fi
  case "$err" in
    *"onnection refused"*) return 1 ;;
    *) return 2 ;;
  esac
}

if [ "$DOWN" = yes ]; then
  echo "==> removing the demo install"
  teardown
  command rm -rf "$WORK"
  echo "    containers, network and $WORK are gone"
  exit 0
fi

command -v go >/dev/null || { echo "go is required (it runs the seeder)" >&2; exit 1; }
if [ -n "$STATE_NAME" ]; then
  sports_state "$STATE_NAME"
  exit 0
fi
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker is not running" >&2; exit 1; }
if [ "$PROOF" = yes ]; then
  sports_proof
  exit 0
fi

# THE HEADER READS "dev" WHEN THERE IS NO RELEASE TAG (a shallow clone), and the
# screenshots exist to look like the product people install. Refuse to take them
# (only when a capture will run: --no-capture and --base never photograph).
if [ "$CAPTURE" = yes ] && [ -z "$BASE" ] && [ "$DEMO_VERSION" = dev ]; then
  echo "no release tag found (shallow clone?): the header would read \"dev\". git fetch --tags, or set DEMO_VERSION." >&2
  exit 1
fi

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

# THE FIRST SAFETY LAYER (sports): before any container or network exists, every
# destination URL in the fixture must be on the receiver's host. A fixture that
# names a real platform stops the script here.
if [ "$SCENE" = sports ]; then
  go run "$DRIVER" - guardfixture "$FIXTURE" "$RECV" >&2 \
    || { echo "REFUSING: the sports fixture has a destination that is not the local receiver ($RECV); nothing was started." >&2; exit 1; }
  echo "==> every fixture destination is the local receiver"
fi

# THE FIRST SAFETY LAYER (full), before any container exists
# (internal/demorig): every alert-rule URL is https on a .invalid host; every
# destination that is not a file is an rtmp, rtmps or srt URL on a .invalid
# host, with no platform account, extra FFmpeg argument or multitrack; and every
# "start" schedule names file destinations only. The fixture has no hooks. The
# driver runs the same check again before the destinations are created. The
# network is internal and nothing is published but the relay, so a fixture
# edit that slips past all of this still has no route out.
if [ "$SCENE" = full ]; then
  go run "$DRIVER" - guardcontent "$FIXTURE" >&2 \
    || { echo "REFUSING: the fixture has an alert URL, a destination or a schedule that could reach a real host; nothing was started." >&2; exit 1; }
  echo "==> every fixture URL is on a .invalid host or a file"
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

# First-run setup needs the server's one-time setup code; preset it so the
# server started below and the seeder share it. See lib-preflight.sh.
if [ -z "${POLYEMESIS_SETUP_CODE:-}" ]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is needed to generate a setup code; export POLYEMESIS_SETUP_CODE yourself" >&2
    exit 1
  }
  POLYEMESIS_SETUP_CODE="demo-$(openssl rand -hex 8)"
fi
export POLYEMESIS_SETUP_CODE

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

# EVERYTHING THAT COULD REFUSE HAS RUN BY NOW OR RUNS HERE, BEFORE ANYTHING IS
# CREATED: a refusal that leaves a work directory behind makes the next run
# refuse as a leftover. Only this rig's own containers are removed first (a
# previous run's relay holds the port), then the port is checked.
teardown
if port_in_use; then
  echo "port $PORT is already in use:" >&2
  if command -v lsof >/dev/null 2>&1; then lsof -nP -iTCP:"$PORT" 2>/dev/null | grep LISTEN >&2 || true; fi
  echo "stop it first, or set DEMO_PORT." >&2
  exit 1
elif [ "$?" = 2 ]; then
  echo "cannot tell whether port $PORT is free (no /dev/tcp?); refusing rather than guessing." >&2
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
command rm -rf "$WORK"
# THE WORK DIRECTORY IS PRIVATE (umask 077): the credential files, the stream
# keys and the proof captures live in it. $OUT is not, and keeps the umask.
( umask 077; mkdir -p "$DATA/recordings" "$STAGE" )
mkdir -p "$OUT"
# THE CREDENTIALS ARE FILES, mode 600, and nothing prints them: --state and
# --proof read them back, and a password in an argument shows in `ps`.
( umask 077
  printf '%s' "$E2E_PASSWORD" > "$WORK/admin.pw"
  printf '%s' "$POLYEMESIS_SETUP_CODE" > "$WORK/setup.code" )

# BUILT EVERY RUN, never reused if present (DEMO_REUSE_IMAGE=1, which the script
# never sets, is for iterating on this script and for its offline tests). capture-media.sh learned this the
# expensive way: the image on the machine was a week old, so every screenshot
# committed from it photographed week-old code. A tool that advertises the
# product must photograph the product as it is now.
if [ "${DEMO_REUSE_IMAGE:-}" = 1 ] && docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "==> reusing the image $IMAGE (DEMO_REUSE_IMAGE=1)"
else
echo "==> building the UI and the demo image"
( cd "$ROOT" && make build >/dev/null ) || { echo "make build failed" >&2; exit 1; }
GOARCH_LOCAL="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_LOCAL" \
    go build -trimpath -ldflags "-s -w -X main.version=$DEMO_VERSION" \
      -o "$STAGE/polyemesis-linux" ./cmd/polyemesis ) \
  || { echo "cross-compile for the container failed" >&2; exit 1; }
# Alpine 3.24, the release image's base: its ffmpeg is 8.x with libsrt. 3.20
# carries FFmpeg 6.1, which the server refuses at startup since FFmpeg 8.0
# became the floor, and the container exits before /health ever answers.
docker build -q -t "$IMAGE" -f - "$STAGE" >/dev/null <<'DOCKERFILE' || { echo "docker build failed" >&2; exit 1; }
FROM alpine:3.24
RUN apk add --no-cache ffmpeg ca-certificates font-dejavu socat
COPY polyemesis-linux /usr/local/bin/polyemesis
ENTRYPOINT ["/usr/local/bin/polyemesis"]
DOCKERFILE
fi

# Server AND publishers in containers on one network, dialing each other by
# name. The host-to-container route does not work here and the reason is
# recorded in capture-media.sh: the publisher reached 127.0.0.1:6000 over IPv4
# while the gosrt listener was bound IPv6-only, and nothing logged a refusal
# because nothing arrived.
echo "==> starting polyemesis on :$PORT"
# Both scenes: the network is --internal and the server is never published with
# -p; the relay started after it is the only thing that binds 127.0.0.1:$PORT.
if [ "$SCENE" = sports ]; then
  sports_network_and_receiver
else
  # Internal, and asked about rather than assumed, before the server exists.
  sports_create_internal_network
fi
docker run -d --rm --name "$SRV" -e POLYEMESIS_SETUP_CODE --network "$NET" \
  -v "$DATA:/data" \
  "$IMAGE" -addr ":$PORT" -data /data -log warn >/dev/null \
  || { echo "the polyemesis server failed to start" >&2; exit 1; }
sports_start_relay

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
if [ "$SCENE" = full ]; then
echo "==> rendering the recording library"
docker run --rm --network none -v "$DATA:/data" --entrypoint ffmpeg "$IMAGE" \
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
  docker run --rm --network none -v "$DATA:/data" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -y \
    -f concat -safe 0 -i /data/.demo-seed/concat.txt \
    -map 0 -c copy -t "$SECONDS_LONG" "/data/recordings/$NAME"
  printf '    %s  %d:%02d\n' "$NAME" $((SECONDS_LONG / 60)) $((SECONDS_LONG % 60))
done < <(go run "$DRIVER" "$BASE_URL" recordings "$FIXTURE")
fi

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
if [ "$SCENE" = sports ]; then
  sports_publish "$PLAN"
else
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
fi

echo "==> waiting for every programme to go live and probe"
go run "$DRIVER" "$BASE_URL" wait "$FIXTURE"

echo "==> creating the rendition ladder and the destinations"
if [ "$SCENE" = full ]; then
  go run "$DRIVER" "$BASE_URL" finish "$FIXTURE" invalid
else
  go run "$DRIVER" "$BASE_URL" finish "$FIXTURE"
fi

# THE FULL SCENE'S AUTOMATION PAGE AND ROLLING BUFFER: alert rules and
# schedules (no hooks, see the fixture), and the first programme's buffer armed
# so it fills while the encoders settle below. The sports scene has none.
if [ "$SCENE" = full ]; then
  echo "==> seeding alert rules, schedules and the rolling buffer"
  go run "$DRIVER" "$BASE_URL" content "$FIXTURE"
fi

# THE SECOND SAFETY LAYER (sports): the same allow-list, against what the server
# stored. The driver's start re-checks before every start.
if [ "$SCENE" = sports ]; then
  echo "==> checking the stored destinations"
  go run "$DRIVER" "$BASE_URL" guard "$FIXTURE" "$RECV"
  go run "$DRIVER" "$BASE_URL" routing "$FIXTURE" initial
fi
# What the capture must refuse to photograph: the fixture's tokens and stream
# keys, then the admin password and the setup code (read from their files, never
# echoed). Both scenes; the file is mode 600.
( umask 077
  go run "$DRIVER" "$BASE_URL" secrets "$FIXTURE" > "$WORK/secrets.list"
  printf 'password\t%s\n' "$(cat "$WORK/admin.pw")" >> "$WORK/secrets.list"
  printf 'setupCode\t%s\n' "$(cat "$WORK/setup.code")" >> "$WORK/secrets.list" )

if [ "$SCENE" = sports ]; then
  echo "==> checking that the publisher holds ${FPS} fps"
  sports_check_publisher
else
# The ladder has to actually start before it is worth photographing: an encoder
# that has not opened yet reports no consumers and no bitrate, and the ladder
# view is the one page where those two numbers are the whole point.
echo "==> letting the encoders and file destinations settle"
sleep 25
echo "==> capturing a clip from the rolling buffer"
go run "$DRIVER" "$BASE_URL" clip "$FIXTURE"
fi

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
  echo "    sign in as admin; credential: $WORK/admin.pw"
  echo "    ./scripts/demo-seed.sh --down  removes all of it"
  exit 0
fi

if [ "$SCENE" = sports ]; then
  echo "==> routing, starting and waiting for the five destinations"
  sports_prepare_capture
fi

echo "==> capturing into $OUT"
# Only PNGs newer than this marker are this run's: the check, the compression and
# the site refresh below leave every other file in $OUT alone.
( umask 077; touch "$WORK/.started" )
( cd "$ROOT/ui" && BASE_URL="$BASE_URL" SHOT_DIR="$OUT" SHOT_SCENE="$SCENE" \
    SHOT_SECRETS_FILE="$WORK/secrets.list" \
    npx --no-install playwright test --config=e2e/capture.config.ts )

[ -f "$ROOT/scripts/check-shots.mjs" ] || {
  echo "scripts/check-shots.mjs is missing, so the shots cannot be checked; refusing to go on." >&2
  exit 1
}
node "$ROOT/scripts/check-shots.mjs" "$SCENE" "$WORK/.started" \
  || { echo "the shots failed their check; nothing was compressed or copied." >&2; exit 1; }

# THE SAME COMPRESSION capture-media.sh has always applied, which this script
# never did -- so its shots came out at 300-450 KB against 90-200 KB for the
# committed ones, and could not be committed as they were. A 256-colour palette
# is lossless to the eye on a UI capture (flat fills, one accent, text); the
# site still serves avif/webp variants built from these by Astro's <Picture>.
echo "==> compressing"
if python3 -c "import PIL" 2>/dev/null; then
  python3 - "$OUT" "$WORK/.started" <<'PY'
import glob, os, sys
from PIL import Image
before = after = 0
started = os.path.getmtime(sys.argv[2])
for path in sorted(glob.glob(os.path.join(sys.argv[1], "*.png"))):
    if os.path.getmtime(path) <= started:
        continue
    was = os.path.getsize(path)
    im = Image.open(path).convert("RGB")
    # To a temp file beside the target, then a rename: a kill mid-save must not
    # leave a truncated PNG at the committed path.
    tmp = path + ".tmp"
    im.quantize(colors=256, method=Image.MEDIANCUT,
                dither=Image.FLOYDSTEINBERG).save(tmp, format="PNG", optimize=True)
    os.replace(tmp, path)
    now = os.path.getsize(path)
    before, after = before + was, after + now
if before:
    print(f"    {before // 1024}K -> {after // 1024}K "
          f"({100 - 100 * after / before:.0f}% smaller, resolution unchanged)")
PY
else
  echo "    skipped: python3 with Pillow not found, shots left unquantised" >&2
fi

# The site keeps its own copies (Astro must see them under web/src to build the
# avif/webp variants), and web/src/lib/features-shots.test.ts fails when they
# differ from docs/media by a byte. So refresh them here, the same way
# capture-media.sh does: only files the site already ships, and only when this
# run wrote the committed set. A verification run into a scratch --out must not
# touch the site.
SITE_SHOTS="$ROOT/web/src/assets/shots"
if [ "$OUT" = "$ROOT/docs/media" ] && [ -d "$SITE_SHOTS" ]; then
  echo "==> refreshing the site's copies"
  copied=0
  for dst in "$SITE_SHOTS"/*.png; do
    [ -e "$dst" ] || continue
    src="$OUT/$(basename "$dst")"
    if [ -f "$src" ] && [ "$src" -nt "$WORK/.started" ] && ! cmp -s "$src" "$dst"; then
      cp -f "$src" "$dst"
      echo "    $(basename "$dst")"
      copied=$((copied + 1))
    fi
  done
  [ "$copied" -eq 0 ] && echo "    already in step"
fi

echo
echo "==> done"
ls -la "$OUT"/*.png
echo
echo "the install is still up at $BASE_URL (admin; credential: $WORK/admin.pw)"
echo "./scripts/demo-seed.sh --down  removes the containers and $WORK"
