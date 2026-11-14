#!/usr/bin/env bash
#
# The sports scene of scripts/demo-seed.sh: one match feed with six audio tracks
# going to five destinations labelled YouTube, which publish only to a receiver
# container on an internal Docker network. Sourced by demo-seed.sh, never run on
# its own; every function uses the variables that script defines (ROOT, DRIVER,
# FIXTURE, IMAGE, WORK, NET, SRV, BASE_URL, ...). The network and relay functions
# are also used by the full scene (they need PROXY, PORT and IMAGE).
#
# NOTHING REACHES A REAL PLATFORM. Three layers, all kept:
#   1. the fixture's destination hosts are checked against an allow-list of one
#      host (the receiver) by `guardfixture`, before any container exists;
#   2. the same check runs against what the server stored, after seeding and
#      before every start (`guard`, and `start` re-checks);
#   3. the server container sits on a network created with --internal, is never
#      published with -p, and the console is reached through a relay container
#      that alone binds 127.0.0.1:$PORT.
#
# CREDENTIALS are files in $WORK (mode 600), never arguments. The publish token
# reaches the publisher container through its environment, so it is not in
# `docker run`'s argument list (it is in the ffmpeg command line inside that
# container, as the full scene's publishers have it).

# Reassigns the names the script defined for the full scene. Called once, after
# argument parsing, so the two scenes can run side by side on their own ports.
# shellcheck disable=SC2034 # these names are read by demo-seed.sh, which sources this file
sports_configure() {
  WORK="${DEMO_WORK:-${TMPDIR:-/tmp}/polyemesis-demo-sports}"
  DATA="$WORK/data"
  STAGE="$DATA/.demo-seed"
  PORT="${DEMO_PORT:-8097}"
  NET="${DEMO_NET:-polyemesis-demo-sports-net}"
  SRV="${DEMO_SRV:-polyemesis-demo-sports-server}"
  PUB_PREFIX="${DEMO_PUB_PREFIX:-polyemesis-demo-sports-pub}"
  FIXTURE="${DEMO_FIXTURE:-$ROOT/scripts/demo-seed.sports.fixture.json}"
  # The receiver's name is the ONE host a destination may use; it is also the
  # host in the fixture's URLs (internal/testenv checks the two agree).
  RECV="${RECV:-polyemesis-demo-recv}"
  # Pinned by tag AND by the multi-platform index digest (read from the pulled
  # image and confirmed against the registry), so a moved tag cannot change it.
  RECV_IMAGE="bluenviron/mediamtx:1.21.3@sha256:59e43552f3be43695b70c19424dc2a42fcec18075f88d91561a24ac5d70a586c"
  PROXY="${DEMO_PROXY:-polyemesis-demo-sports-proxy}"
}

# Asked before a network is created, so a refused engine leaves none behind.
sports_require_modern_engine() {
  # Docker Engine before 26.0.0 forwarded DNS queries from an internal network to
  # the host's resolver (CVE-2024-29018), so an --internal network was not quite
  # closed. Refuse an older (or unreadable) engine rather than trust the flag.
  local ver major
  ver="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  major="${ver%%.*}"
  case "$major" in
    ''|*[!0-9]*) echo "REFUSING: cannot read the Docker Engine version; engines older than 26.0.0 forward DNS from internal networks." >&2; exit 1 ;;
  esac
  if [ "$major" -lt 26 ]; then
    echo "REFUSING: Docker Engine $ver is older than 26.0.0, which forwarded DNS from internal networks to the host's resolver; update Docker." >&2
    exit 1
  fi
}

# THE THIRD SAFETY LAYER, asked of Docker rather than assumed: the rig's network
# must be internal. Used after creating it, and before anything acts on a rig
# that is already running (--state start and routing, --proof).
sports_require_internal_network() {
  sports_require_modern_engine
  [ "$(docker network inspect -f '{{.Internal}}' "$NET" 2>/dev/null)" = true ] \
    || { echo "REFUSING: network $NET is not internal (or cannot be inspected); it could route off this machine." >&2; exit 1; }
}

# The internal network, checked to BE internal. Both scenes use it; a network
# that already exists (DEMO_NET naming a bridge) fails the create and is refused.
sports_create_internal_network() {
  sports_require_modern_engine
  docker network create --internal "$NET" >/dev/null \
    || { echo "could not create the network $NET" >&2; exit 1; }
  # Not trusting the flag: ask Docker what it made.
  sports_require_internal_network
}

# The internal network, then the receiver on it.
sports_network_and_receiver() {
  sports_create_internal_network
  docker run -d --name "$RECV" --network "$NET" "$RECV_IMAGE" >/dev/null \
    || { echo "the receiver $RECV failed to start" >&2; exit 1; }
}

# The console is reached through a tiny TCP relay, so the server itself can stay
# on the internal network with nothing published. The relay starts on the
# default network (the only kind a port can be published from) and is attached
# to the internal one afterwards.
sports_start_relay() {
  if [ -n "$(docker port "$SRV" 2>/dev/null)" ]; then
    echo "REFUSING: the server container publishes a host port; only the relay may." >&2
    exit 1
  fi
  docker run -d --name "$PROXY" -p "127.0.0.1:$PORT:$PORT" --entrypoint socat "$IMAGE" \
    "TCP-LISTEN:$PORT,fork,reuseaddr" "TCP:$SRV:$PORT" >/dev/null \
    || { echo "the console relay failed to start" >&2; exit 1; }
  docker network connect "$NET" "$PROXY" >/dev/null \
    || { echo "could not attach the relay to $NET" >&2; exit 1; }
}

# The match feed: the pitch is drawn once to a PNG, then looped at $FPS with the
# filter graph in scripts/demo-sports/match.filter (players, ball, scorebug and
# six synthetic audio tracks). Through the real SRT ingest, told apart by the
# token in the streamid, so the dashboard reads a working product.
#   sports_publish <plan.tsv>      (the `setup` output; column 2 is the token)
#
# The plan line is id<TAB>token<TAB><TAB>9000<TAB> for this scene (no lavfi
# fields). `read` with IFS=tab would collapse the empty fields and shift the
# rest, so the token is taken by awk with an explicit tab separator, and nothing
# else on the line is used.
#   sports_plan_field <plan.tsv> <n>   field n of the first line, empty ones kept
sports_plan_field() {
  awk -F'\t' -v n="$2" 'NR==1{print $n}' "$1"
}
sports_plan_token() { sports_plan_field "$1" 2; }

sports_publish() {
  local token script kbps
  token="$(sports_plan_token "$1")"
  [ -n "$token" ] || { echo "the seeder returned no publish token" >&2; exit 1; }
  # The video bitrate is the fixture's (video.kbps), carried in field 4.
  kbps="$(sports_plan_field "$1" 4)"
  case "$kbps" in ''|*[!0-9]*) echo "the plan line has no video bitrate in field 4" >&2; exit 1 ;; esac
  ( umask 077; mkdir -p "$WORK/pub" )
  # shellcheck disable=SC2016 # expanded by the container's shell, from its environment
  script='ffmpeg -hide_banner -loglevel error -y -filter_complex_script /m/pitch.filter -map "[v]" -frames:v 1 /w/pitch.png &&
exec ffmpeg -hide_banner -loglevel warning -nostats -progress /w/progress.txt -stats_period 5 \
  -loop 1 -framerate "$FPS" -i /w/pitch.png -filter_complex_script /m/match.filter \
  -map "[v]" -map "[a0]" -map "[a1]" -map "[a2]" -map "[a3]" -map "[a4]" -map "[a5]" \
  -c:v libx264 -preset veryfast -tune zerolatency -g "$GOP" -pix_fmt yuv420p \
  -b:v "${KBPS}k" -maxrate "${MAXKBPS}k" -bufsize "${BUFKBPS}k" -c:a aac -b:a 128k \
  -metadata:s:a:0 title=English -metadata:s:a:1 title=Español -metadata:s:a:2 title=Português \
  -metadata:s:a:3 title=Français -metadata:s:a:4 title=Arabic -metadata:s:a:5 title=Crowd \
  -f mpegts "srt://$SRV_HOST:$SRT_PORT?streamid=$PUB_TOKEN&mode=caller&transtype=live&latency=200000"'
  PUB_TOKEN="$token" docker run -d --name "$PUB_PREFIX-match" --network "$NET" \
    -v "$ROOT/scripts/demo-sports:/m:ro" -v "$WORK/pub:/w" \
    -e PUB_TOKEN -e "KBPS=$kbps" -e "MAXKBPS=$(( kbps * 19 / 18 ))" -e "BUFKBPS=$(( kbps * 2 ))" -e "FPS=${FPS:-60}" -e "GOP=$(( ${FPS:-60} * 2 ))" \
    -e "SRV_HOST=$SRV" -e "SRT_PORT=${SRT_PORT:-6000}" \
    --entrypoint sh "$IMAGE" -c "$script" >/dev/null \
    || { echo "the match publisher failed to start" >&2; exit 1; }
  PUB_T0="$(date +%s)"
  echo "    match feed publishing (6 audio tracks, ${FPS:-60} fps)"
}

# 0 when the publisher kept up, else 1 with the hint. "Kept up": the last three
# speed readings are at least 0.98 and no frame was dropped. A machine that
# cannot hold the rate would otherwise photograph a feed that stutters.
publisher_speed_ok() {
  local file="$1" speeds drops lowest
  speeds="$(grep '^speed=' "$file" 2>/dev/null | tail -n 3 | sed 's/^speed=//; s/x//; s/ //g')"
  drops="$(grep '^drop_frames=' "$file" 2>/dev/null | tail -n 1 | sed 's/^drop_frames=//')"
  lowest="$(printf '%s\n' "$speeds" | awk 'NF { v = $1 + 0; if (n == 0 || v < m) m = v; n++ } END { if (n < 3) print "n/a"; else print m }')"
  if [ "$lowest" != "n/a" ] && [ "${drops:-x}" = 0 ] \
    && awk -v v="$lowest" 'BEGIN { exit (v >= 0.98) ? 0 : 1 }'; then
    return 0
  fi
  echo "this machine cannot hold ${DEMO_FPS:-60} fps (speed ${lowest}, dropped ${drops:-unknown}). Re-run with DEMO_FPS=30." >&2
  return 1
}

# Called once the feed is live: waits until the publisher has run 20 s, then
# judges it. A refusal stops the script and leaves the rig for --down.
sports_check_publisher() {
  local left=$(( PUB_T0 + 20 - $(date +%s) ))
  [ "$left" -gt 0 ] && sleep "$left"
  publisher_speed_ok "$WORK/pub/progress.txt" || exit 1
  echo "    publisher kept up: $(grep '^fps=' "$WORK/pub/progress.txt" | tail -n 1) $(grep '^speed=' "$WORK/pub/progress.txt" | tail -n 1) $(grep '^drop_frames=' "$WORK/pub/progress.txt" | tail -n 1)"
}

# What the capture needs: the shots are of the routed, live state.
sports_prepare_capture() {
  go run "$DRIVER" "$BASE_URL" routing "$FIXTURE" final
  go run "$DRIVER" "$BASE_URL" start "$FIXTURE" "$RECV"
  go run "$DRIVER" "$BASE_URL" waitlive "$FIXTURE"
}

# Acts on the running rig and exits; the password is read from its file.
#   sports_state routing-initial|routing-final|start|stop|status|guard|waitlive
sports_state() {
  local name="$1"
  umask 077
  if [ ! -s "$WORK/admin.pw" ]; then
    echo "no sports rig is running (there is no $WORK/admin.pw)." >&2
    echo "start one with: scripts/demo-seed.sh --scene sports --no-capture" >&2
    exit 1
  fi
  E2E_PASSWORD="$(cat "$WORK/admin.pw")"
  export E2E_PASSWORD
  case "$name" in
    routing-initial|routing-final|start)
      command -v docker >/dev/null 2>&1 || { echo "docker is required to check the rig's network" >&2; exit 1; }
      sports_require_internal_network ;;
  esac
  case "$name" in
    routing-initial) go run "$DRIVER" "$BASE_URL" routing "$FIXTURE" initial ;;
    routing-final) go run "$DRIVER" "$BASE_URL" routing "$FIXTURE" final ;;
    start) go run "$DRIVER" "$BASE_URL" start "$FIXTURE" "$RECV" ;;
    stop) go run "$DRIVER" "$BASE_URL" stop "$FIXTURE" ;;
    status) go run "$DRIVER" "$BASE_URL" status "$FIXTURE" ;;
    guard) go run "$DRIVER" "$BASE_URL" guard "$FIXTURE" "$RECV" ;;
    waitlive) go run "$DRIVER" "$BASE_URL" waitlive "$FIXTURE" ;;
    *) echo "unknown state: $name" >&2; exit 2 ;;
  esac
}

# Reads 15 s of each destination's path back from the receiver, from inside the
# network (ffprobe for the layout, raw audio and ebur128 for the sound), then
# lets proof.py say whether each path carries its own language. Needs the five
# destinations live. The stream keys come from secrets.list and are passed to
# the containers through their environment; the saved logs are masked.
sports_proof() {
  local dir="$WORK/proof" kind key lang
  local langs=(en es pt fr ar) i=0
  local proof_pids=() proof_pid
  umask 077
  sports_require_internal_network
  [ -s "$WORK/secrets.list" ] || { echo "no $WORK/secrets.list: is the rig up?" >&2; exit 1; }
  command rm -rf "$dir"; mkdir -p "$dir"
  # The layout first: each path must carry exactly one H.264 1920x1080 video
  # stream and one audio stream, or the proof stops naming the path.
  while IFS=$'\t' read -r kind key; do
    [ "$kind" = streamkey ] || continue
    [ "$i" -lt "${#langs[@]}" ] || break
    lang="${langs[$i]}"; i=$((i + 1))
    PROOF_URL="rtmp://$RECV:1935/live/$key" docker run --rm --network "$NET" -e PROOF_URL --entrypoint sh "$IMAGE" -c \
      'exec ffprobe -v error -of json -show_entries stream=codec_type,codec_name,width,height "$PROOF_URL"' \
      > "$dir/probe-$lang.json" 2>/dev/null || echo '{}' > "$dir/probe-$lang.json"
    python3 - "$dir/probe-$lang.json" "$lang" <<'PY' || exit 1
import json, sys
path, lang = sys.argv[1], sys.argv[2]
streams = json.load(open(path)).get("streams", [])
video = [s for s in streams if s.get("codec_type") == "video"]
audio = [s for s in streams if s.get("codec_type") == "audio"]
ok = (len(video) == 1 and len(audio) == 1 and len(streams) == 2
      and video[0].get("codec_name") == "h264"
      and (video[0].get("width"), video[0].get("height")) == (1920, 1080))
if not ok:
    found = ", ".join("%s %s %sx%s" % (s.get("codec_type"), s.get("codec_name"), s.get("width", "-"), s.get("height", "-")) for s in streams) or "no streams"
    print("layout: path %s is not one h264 1920x1080 video plus one audio stream; found: %s" % (lang, found), file=sys.stderr)
    sys.exit(1)
print("layout %s: h264 1920x1080 + 1 audio" % lang)
PY
  done < "$WORK/secrets.list"
  [ "$i" -eq "${#langs[@]}" ] || { echo "expected ${#langs[@]} stream keys in secrets.list, found $i" >&2; exit 1; }
  i=0
  while IFS=$'\t' read -r kind key; do
    [ "$kind" = streamkey ] || continue
    [ "$i" -lt "${#langs[@]}" ] || break
    lang="${langs[$i]}"; i=$((i + 1))
    (
      export PROOF_URL="rtmp://$RECV:1935/live/$key"
      docker run --rm --network "$NET" -e PROOF_URL -v "$dir:/p" --entrypoint sh "$IMAGE" -c \
        "exec ffmpeg -hide_banner -nostats -y -t 15 -i \"\$PROOF_URL\" -map 0:a:0 -f s16le -ac 2 -ar 48000 /p/$lang.pcm -map 0:a:0 -af ebur128=peak=true:framelog=quiet -f null -" \
        > "$dir/ebur-$lang.log" 2>&1 || true
    ) &
    proof_pids+=("$!")
  done < "$WORK/secrets.list"
  # Each reader by its pid: a bare `wait` would also block on any other job
  # this shell has. Every reader ends itself (`-t 15`, `|| true`).
  for proof_pid in ${proof_pids[@]+"${proof_pids[@]}"}; do
    wait "$proof_pid" || true
  done
  # ffmpeg echoes its input URL, which ends in the stream key.
  for f in "$dir"/*.log; do
    sed 's#/live/[^ ]*#/live/(key)#g' "$f" > "$f.masked" && mv "$f.masked" "$f"
  done
  python3 "$ROOT/scripts/demo-sports/proof.py" "$dir"
}
