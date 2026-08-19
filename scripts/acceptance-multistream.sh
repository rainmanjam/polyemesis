#!/usr/bin/env bash
# Multistream acceptance: one source fans out to four platforms at once, and
# EACH ONE RECEIVES THE MIX ITS OWN routing.Profile NAMES.
#
# NOT WIRED INTO .github/workflows/ci.yml, AND THAT IS DELIBERATE. Run with real
# keys this publishes to a real account: followers are notified, a VOD may be
# recorded, the channel can appear in a directory. That is an operational
# decision a maintainer takes, not something a push to a branch takes on their
# behalf. Local, or workflow_dispatch with the keys as secrets. The DRY RUN path
# below needs no key at all and is what CI could run if anyone ever wants it to.
#
# WHY THIS SUITE EXISTS
#
# Issue #141, rescoped. The original question -- "does Twitch's E-RTMP ingest
# accept a SECOND AUDIO TRACK" -- has a boring answer. No platform documents
# accepting one: YouTube ignores multitrack, Twitch's Enhanced Broadcasting uses
# multitrack for VIDEO renditions, Amazon IVS announced multitrack VIDEO ingest.
# Multitrack audio at ingest has essentially no adoption anywhere.
#
# Which means the correct product behaviour is not to send two tracks. It is to
# MIX PER DESTINATION and hand each platform the one mix it should receive --
# which is what this product already does, and which has never been measured end
# to end. So the useful question is the one next door:
#
# STILL TRUE, WITH ONE CORRECTION. "No platform documents accepting a second
# audio track" is a statement about platforms and it stands. It was also read
# for years as "polyemesis cannot send one", which was never measured either
# way. It can: internal/ffmpeg.TestTwoDistinctMixesReachAnRTMPFarEnd publishes
# two mixes through this product's own RTMP server and reads both tones back off
# the far end. The default is still one track, per the paragraph above.
#
#   does one source fan out to Twitch + YouTube + Kick + Facebook at once, each
#   receiving its own correct mix?
#
# THE FAILURE THIS SUITE IS SHAPED AROUND
#
# Per-destination routing silently sending the SAME MIX EVERYWHERE. From the
# sending side that is indistinguishable from success: four processes up, four
# platforms ingesting, four green cards, four healthy bitrates. Nothing on the
# dashboard is wrong. The operator finds out when a viewer says the commentary
# is missing.
#
# "The destination is delivering bytes" cannot see it. Neither can "the profile
# was saved correctly". The only thing that can is a measurement of WHAT ARRIVED
# at each far end, compared BETWEEN far ends. So the ingest carries two audio
# tracks at two well-separated tones, each destination's profile selects a
# different subset, and every check below is either "this destination received
# the tone its profile names" or "these two destinations received DIFFERENT
# audio".
#
# TWO PLATFORMS DELIBERATELY SHARE A PROFILE (twitch and facebook, both track 0)
# and must therefore MATCH. Without that pair the suite would only ever reward
# difference, and a routing bug that scrambled destinations at random -- sending
# each one somebody else's mix -- would pass every "these differ" check.
#
# ONE FAR END IS NOT AN FFMPEG LISTENER. IT IS THIS PRODUCT'S OWN RTMP INGEST.
#
# Every sink above is `ffmpeg -f flv -listen 1`, and that is an extremely
# permissive receiver: it takes almost any bytes offered to it, on any playpath,
# with no notion of whether the publisher was entitled to be there. It can say
# "a stream arrived". It cannot say "a stream a real RTMP SERVER would accept
# arrived", because it has no way to refuse one.
#
# internal/rtmpserver is a real one. It performs the handshake and addresses
# sources BY STREAM KEY -- Lookup(streamKey) (Target, bool), StreamKey(u) -- and
# a publish whose key it does not recognise is refused at the handshake, the way
# Twitch or YouTube refuses a bad key. So step 4b creates a SECOND source with
# its own publish token, points one extra destination at
# rtmp://127.0.0.1:<ingest>/live/<that token>, and measures whether the product
# ACCEPTED its own output. That is a claim none of the four sinks can support.
#
# AND IT IS MADE FALSIFIABLE IN THE SAME BREATH. "Accepted" means nothing from a
# receiver that accepts everything, so the same listener is offered a WRONG key
# at the same moment and must refuse it. Without that half, a loopback check is
# unfalsifiable and would pass against a server with no key checking at all.
#
# THE LOOPBACK SOURCE HAS NO DESTINATIONS, AND MUST NOT. A far end that fans out
# is a far end that can fan back into the source feeding it; RTMP will carry
# that cycle happily, and each lap adds a full encode. The suite asserts the
# count is zero rather than trusting that nobody adds one later.
#
# WHAT THE DRY RUN PROVES AND WHAT IT DOES NOT
#
# With no key set, all four "platforms" are local RTMP listeners and every check
# is measured on real bytes off a real wire. That proves the routing, the fan-out
# and this harness. It proves nothing about any platform's ingest, and the final
# banner says so in as many words: a dry pass must never be read as a live pass.
#
# WHAT A LIVE RUN CAN AND CANNOT MEASURE
#
# With a real key, the platform destination publishes to the platform and the
# far end is not readable from here -- nothing local can hear what Twitch
# received. So a live platform is measured on what IS observable:
#
#   * it connected, stayed up and produced media (no restarts: a platform that
#     refuses a stream drops the connection and the supervisor respawns)
#   * routing compiled the tracks its profile names, read back from
#     engine.DestStatus.Tracks
#
# and the RECEIVED-AUDIO half is measured on a LOCAL TWIN destination carrying
# the same profile. The twin is a separate process publishing the same mix to a
# sink we can read. It proves "this profile produces this mix"; it does NOT
# prove "Twitch received this mix", and every PASS line for it says so.
# Confirming what a platform received needs that platform's own playback or API
# and is out of scope here -- see the report on the PR.
#
# CREDENTIALS
#
#   TWITCH_STREAM_KEY  YOUTUBE_STREAM_KEY  KICK_STREAM_KEY  FACEBOOK_STREAM_KEY
#
# Environment only. Never an argument, never a file, never a prompt. Process
# arguments are world-readable through ps(1), so a key on a command line is
# disclosed to every local user for as long as the command runs. Nothing in this
# file interpolates a key into a command; the driver reads them with os.Getenv
# and POSTs them over loopback, and steps 8a-8d verify that afterwards by
# looking for the value in the places it could have escaped to.
#
# A platform with no key is SKIPPED AND COUNTED AS SKIPPED. It is never silently
# passed, and the check total below cannot be reached by skipping -- see
# EXPECTED_CHECKS and internal/testenv/testdata/skips.json for why this
# repository is careful about exactly that.
#
# Ingest endpoints default to each platform's published address and are
# overridable, because two of them are not universal: Kick issues a per-channel
# ingest host, so KICK_INGEST_URL has no default and a key without one is a
# reported skip rather than a guess.
#
#   TWITCH_INGEST_URL    default rtmp://live.twitch.tv/app
#   YOUTUBE_INGEST_URL   default rtmp://a.rtmp.youtube.com/live2
#   FACEBOOK_INGEST_URL  default rtmps://live-api-s.facebook.com:443/rtmp
#   KICK_INGEST_URL      no default; required when KICK_STREAM_KEY is set
#
# Usage:  ./scripts/acceptance-multistream.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-multistream}"
PORT=8098
# RTMP, not SRT, for acceptance-failover.sh's reason: the host FFmpeg (Homebrew)
# is built without libsrt and can neither listen nor publish on SRT. What this
# suite measures -- which mix each destination receives -- is independent of how
# the bytes arrived, and SRT ingest is covered by the container suites.
INGEST=1939
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"
. "$SCRIPTS/lib-watchdog.sh"
. "$SCRIPTS/lib-observe.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

# BUILT HERE, for acceptance-failover.sh's reason: a suite that runs whatever
# binary happened to be lying in the repo root is testing a different program
# from the one under review, and that hid a real ingest regression for a session.
go build -o "$BIN" "$ROOT/cmd/polyemesis" || { echo "cannot build polyemesis"; exit 1; }

pass=0; fail=0; skip=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
sk()   { printf "  \033[33mSKIP\033[0m  %s\n" "$1"; skip=$((skip+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }
note() { printf "        %s\n" "$1"; }

cleanup() {
  pkill -f "multistream-publisher" 2>/dev/null
  pkill -f "multistream-sink" 2>/dev/null
  # The wrong-key prober is expected to be dead long before this -- being
  # refused is the whole point of it -- but a run that aborts between spawning
  # it and reaping it would otherwise leave an encoder hammering the ingest.
  pkill -f "multistream-wrongkey" 2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK" "$INGEST"
}
trap 'poly_teardown_trap $? cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
command -v ffprobe >/dev/null || { echo "ffprobe is required"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK" || exit 1
poly_watchdog_arm
WORK="$(pwd)"
# Absolute, for the reason acceptance-failover.sh records at length: a relative
# --data breaks the playlist tier's concat paths outright.
DATA="$WORK/data"
mkdir -p "$DATA/recordings"

# BUILT FROM $ROOT, IN A SUBSHELL, for the reason acceptance-failover.sh
# records at length: the driver imports scripts/internal/driverlib, `go build`
# resolves a module import against the current directory's go.mod, and this
# line runs after the cd into $WORK -- which is outside the module.
DRIVER="$WORK/multistream-driver"
( cd "$ROOT" && go build -o "$DRIVER" "$SCRIPTS/acceptance_multistream_driver.go" ) || {
  echo "cannot build the driver"; exit 1; }
drive() { "$DRIVER" "http://127.0.0.1:$PORT" "$@" 2>&1; }

# ---------------------------------------------------------------- the fixture

# TWO TONES, FAR APART, ONE PER INGEST TRACK.
#
# 300 Hz and 5000 Hz because a bandpass has to separate them cleanly after an
# AAC round trip at 160 kbps; neighbouring tones would leave the "this band is
# absent" checks measuring the filter's skirt rather than the routing. The same
# two frequencies the container suites use, for the same reason.
TONE_A=300     # ingest track 0
TONE_B=5000    # ingest track 1
BAND_A=100     # bandpass width at TONE_A
BAND_B=400     # bandpass width at TONE_B

# THE PLATFORM TABLE. One line per platform:
#
#   <name> <key-env> <url-env> <default-url> <tracks> <sink-port>
#
# tracks is the profile's selection over the ingest's two tracks, and the three
# distinct values are the entire experiment:
#
#   twitch    track 0     -> must carry TONE_A and not TONE_B
#   youtube   track 1     -> must carry TONE_B and not TONE_A
#   kick      tracks 0,1  -> must carry BOTH, at comparable level
#   facebook  track 0     -> same profile as twitch, so it must MATCH twitch
#
# facebook duplicating twitch is not laziness. It is the control: without a pair
# that must come out THE SAME, a suite that only ever demands difference would
# pass a routing bug that handed every destination somebody else's mix.
PLATFORMS="twitch youtube kick facebook"
plat_field() { # plat_field <platform> <field-index>
  # The index is copied out FIRST. `set --` below replaces the positional
  # parameters, so a later read of $2 would return the table's second column
  # instead of the caller's index -- a wrong answer that looks like a right one,
  # since both are strings.
  local idx="$2"
  case "$1" in
    twitch)   set -- TWITCH_STREAM_KEY   TWITCH_INGEST_URL   "rtmp://live.twitch.tv/app"                0   19361 ;;
    youtube)  set -- YOUTUBE_STREAM_KEY  YOUTUBE_INGEST_URL  "rtmp://a.rtmp.youtube.com/live2"          1   19362 ;;
    kick)     set -- KICK_STREAM_KEY     KICK_INGEST_URL     ""                                         0,1 19363 ;;
    facebook) set -- FACEBOOK_STREAM_KEY FACEBOOK_INGEST_URL "rtmps://live-api-s.facebook.com:443/rtmp" 0   19364 ;;
    *) echo ""; return 1 ;;
  esac
  eval "printf '%s\n' \"\${$idx}\""
}
key_env()  { plat_field "$1" 1; }
url_env()  { plat_field "$1" 2; }
def_url()  { plat_field "$1" 3; }
tracks_of(){ plat_field "$1" 4; }
sink_port(){ plat_field "$1" 5; }

# ------------------------------------------------------------------ mode

# LIVE the moment ANY real key is set; DRY otherwise. Deciding per platform
# rather than globally would let a single key turn the other three into silent
# passes, which is precisely the class internal/testenv/testdata/skips.json
# exists to ratchet down.
MODE=dry
for p in $PLATFORMS; do
  env_name="$(key_env "$p")"
  # Indirect expansion, and note what is NOT happening: the VALUE is never
  # placed on a command line, never echoed, never written down. `[ -n ... ]` is
  # a shell builtin, so even this test spawns no process whose argv could carry
  # it.
  [ -n "${!env_name:-}" ] && MODE=live
done

# THE DRY KEY IS SYNTHETIC AND IS GENERATED HERE.
#
# Not a constant, and not omitted. A fixed literal would be indistinguishable
# from a leak of somebody else's; omitting it would leave steps 8a-8d with
# nothing to search for, so the leak guards would report SAFE having looked for
# nothing. 32 hex characters is longer than alerts.MinSecretLen and shaped like
# a real platform key.
#
# `export` rather than an argument, for the same reason the real ones are: the
# driver reads it with os.Getenv and it never reaches an argv.
POLY_DRY_STREAM_KEY="$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')"
export POLY_DRY_STREAM_KEY

# ------------------------------------------------------------------ helpers

# publish starts the encoder standing in for OBS: one video track and TWO audio
# tracks at the two tones, muxed as FLV so FFmpeg emits Enhanced RTMP multitrack.
#
# `-f flv`, never `-f mpegts`, on an rtmp:// URL. Pointing a TS muxer at an RTMP
# connection sends transport-stream bytes down it and the server drops the
# session with "invalid message type: 255" -- which reads exactly like a routing
# failure several checks later.
#
# Named through -metadata comment so cleanup and the argv guard can find it.
# THE SOURCE IS A REAL BROADCAST SHAPE, and was not always.
#
# This published 640x360 at 30fps and 1200 kbps for a long time, for no recorded
# reason -- an unexamined default rather than a decision. It made the suite's
# name a slight lie: "measure real multistream fan-out" was measuring the fan-out
# of a signal no broadcast resembles. Kick's own dashboard recommends 1920x1080
# at 60fps and 8000 kbps; every platform in internal/services publishes a ceiling
# (Twitch 6000, Facebook 9000, Kick 8000) and 1200 came within sight of none of
# them, so nothing here had ever exercised one.
#
# 6000 kbps because that is exactly Twitch's published maximum: at the ceiling
# rather than over it, so a refusal means something is wrong rather than that we
# asked for too much.
#
# THE COST IS ONE ENCODE, NOT NINE. Destinations are -c:v copy -- video passes
# through untouched and only audio is mixed per destination -- so raising the
# source costs a single libx264 process. Measured on the 6-core Haswell this
# suite runs on:
#
#   640x360   30fps 1200k   10s of video encoded in 0.3s   33x realtime
#   1920x1080 30fps 6000k   10s of video encoded in 1.5s  6.7x realtime
#   1920x1080 60fps 6000k   10s of video encoded in 2.7s  3.7x realtime
#
# What this newly puts under test is the network: four destinations at 6000 kbps
# is ~24 Mbit/s sustained upstream, against ~5 at the old size.
MS_WIDTH="${POLY_MS_WIDTH:-1920}"
MS_HEIGHT="${POLY_MS_HEIGHT:-1080}"
MS_FPS="${POLY_MS_FPS:-60}"
MS_VBITRATE="${POLY_MS_VBITRATE:-6000k}"

publish() { # publish <seconds>
  # GOP DERIVED, NOT CONSTANT. Every platform in the registry publishes
  # keyintSeconds: 2, and this was a literal -g 60 back when the source was
  # 30fps -- correct then, and silently wrong the moment the rate changed, since
  # 60 frames at 60fps is a one-second GOP. Deriving it means the framerate is
  # the only thing to get right.
  local gop=$(( MS_FPS * 2 ))
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=${MS_WIDTH}x${MS_HEIGHT}:rate=$MS_FPS" \
    -f lavfi -i "sine=frequency=$TONE_A:sample_rate=48000" \
    -f lavfi -i "sine=frequency=$TONE_B:sample_rate=48000" \
    -metadata comment=multistream-publisher \
    -map 0:v -map 1:a -map 2:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g "$gop" -pix_fmt yuv420p -b:v "$MS_VBITRATE" \
    -c:a aac -b:a 128k -ac 2 -t "$1" \
    -f flv "rtmp://127.0.0.1:$INGEST/live/$PUBKEY" \
    > "publisher.log" 2>&1 &
}

# sink starts one local RTMP listener and records everything published to it.
#
# `-i rtmp://0.0.0.0:PORT/live` WITHOUT a stream key, and that is required
# rather than sloppy: the destination publishes to /live/<key>, and the sink
# must accept a playpath it was never told -- measured, FFmpeg's listener does.
# Spelling the key here would put it on the sink's argv, which is the one thing
# this suite must not do.
#
# `-map 0` so a multi-track arrival is recorded whole. A destination should send
# ONE mixed track; recording only the first would make "it sent two" invisible.
sink() { # sink <platform>
  local port; port="$(sink_port "$1")"
  ffmpeg -hide_banner -loglevel error -f flv -listen 1 \
    -i "rtmp://0.0.0.0:$port/live" \
    -map 0 -c copy -metadata comment=multistream-sink \
    -y "recv-$1.mkv" > "sink-$1.log" 2>&1 &
}

# rms <file> <freq> <width> -> overall RMS dBFS inside that band, or "" .
rms() {
  [ -s "$1" ] || { printf ''; return; }
  ffmpeg -hide_banner -nostats -i "$1" \
    -af "bandpass=f=$2:width_type=h:w=$3,astats=metadata=1:reset=0" -f null - 2>&1 \
    | grep 'RMS level dB' | tail -1 | sed 's/.*: *//'
}
# Float comparison on dB figures, which are negative and fractional.
gt() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 > b+0)}'; }
plus() { awk -v x="$1" -v y="$2" 'BEGIN{print x+y}'; }
absdiff() { awk -v a="$1" -v b="$2" 'BEGIN{d=a-b; if(d<0)d=-d; print d}'; }
isnum() { case "$1" in ''|*[!0-9.+-]*) return 1 ;; *) return 0 ;; esac }

# TONE_FLOOR is "this band is really present", in absolute dBFS.
#
# A single full-scale sine reads around -3 dBFS in its own band; two summed at
# unity through routing's mixer read around -9. -45 is far below either and far
# above the -60-and-under an AAC round trip leaves in an EMPTY band, so it
# separates presence from leakage without being tuned to one machine's build.
TONE_FLOOR=-45
# TONE_MARGIN is how far the excluded band must sit below the selected one for
# "this destination did not receive the other track" to be a statement rather
# than a hope. 20 dB is a factor of ten in amplitude.
TONE_MARGIN=20
# TONE_MATCH is how close two bands must be to count as "both present at
# comparable level" (the kick case) and how close two RECORDINGS must be to
# count as the same mix (the twitch/facebook control).
TONE_MATCH=12
# PROFILE_SPREAD is how far apart two DIFFERENT profiles' recordings must sit,
# measured as the difference of their band balances. Two identical mixes score
# 0; track-0-only against track-1-only scores the sum of both margins.
PROFILE_SPREAD=30

# ------------------------------------------------------- 1. server and ingest

step "1. Server, ingest and publisher"
"$BIN" -addr ":$PORT" -data "$DATA" -log info > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }

OUT=$(drive setup "$INGEST")
case "$OUT" in *SETUP_OK*)  ok "first-run setup" ;;  *) bad "setup: $OUT"; exit 1 ;; esac
case "$OUT" in *INGEST_OK*) ok "ingest on RTMP port $INGEST" ;; *) bad "ingest: $OUT"; exit 1 ;; esac

# THE PUBLISH TOKEN. One shared RTMP listener for the install, addressed by
# token: a keyless publish reaches nothing and dies with a broken pipe several
# checks later, looking like a fan-out fault. Read from the API rather than
# pinned, so a change of shape follows rather than silently breaks.
PUBKEY=$(drive publishkey 2>&1 | tail -1)
case "$PUBKEY" in
  ""|*" "*|*fail*|*FAIL*|driver:*)
    bad "could not read the source publish token: $PUBKEY"; exit 1 ;;
  *) ok "publish token read from the API ($(printf %s "$PUBKEY" | cut -c1-4)…)" ;;
esac

# THE LOOPBACK FAR END'S SOURCE, CREATED HERE AND NOT LATER.
#
# Before any destination exists, deliberately. Creating a source calls
# Manager.Reconcile, and a reconcile that lands while four destination children
# are running is a reconcile that could restart one -- which step 4 would then
# report as "the far end is dropping it", a confident accusation against a
# platform for something this suite did to itself. Nothing is running yet at
# this point, so there is nothing for it to disturb.
#
# It also gives engine.reconcileIngest time to spawn this source's ingest child
# and have it SUBSCRIBE to the shared listener before anything publishes to it.
# rtmpserver refuses a publish to a target with no subscriber (Target.Ready) and
# only waits out the readiness grace for one that is Pending, so a loopback
# destination created while the subscriber is still coming up would be refused
# once and respawned -- and step 4b's restart check would read that as the
# product rejecting its own output.
LOOPSRC=loopback
LOOPID=$(drive addsource "$LOOPSRC" 2>&1 | tail -1)
case "$LOOPID" in
  ''|*[!0-9]*) bad "could not create the loopback source: $LOOPID"; exit 1 ;;
esac
# Exported, never argv, for the reason every other key here is: the driver reads
# it with os.Getenv. It is this install's own token for this run rather than an
# operator's credential -- see srcToken in the driver for why reading it back is
# a different act from reading a platform key.
LOOPKEY=$(drive srctoken "$LOOPSRC" 2>&1 | tail -1)
case "$LOOPKEY" in
  ""|*" "*|driver:*) bad "could not read the loopback source's publish token"; exit 1 ;;
esac
export POLY_LOOPBACK_KEY="$LOOPKEY"

poly_wait_port_ready "$INGEST" 15 || true
# RESOLVED HERE, not beside the sleep it used to live next to: the publisher's
# duration is derived from it and the publisher starts long before the
# measurement window opens.
MS_RUNTIME="${POLY_MS_RUNTIME:-}"
if [ -z "$MS_RUNTIME" ]; then
  if [ "$MODE" = dry ]; then MS_RUNTIME=20; else MS_RUNTIME=75; fi
fi
case "$MS_RUNTIME" in ''|*[!0-9]*) bad "POLY_MS_RUNTIME must be whole seconds, got: '$MS_RUNTIME'"; exit 1 ;; esac
[ "$MS_RUNTIME" -ge 5 ] || { bad "POLY_MS_RUNTIME must be at least 5s, got: $MS_RUNTIME"; exit 1; }

# DERIVED FROM THE WINDOW, NOT CONSTANT. This was a literal `publish 150` --
# the default live window of 75s plus 75s of headroom, long enough for every
# destination to run its measurement window, be stopped, and for the sinks to
# finalise, with the publisher still alive at the end so step 9 can tell "the
# routing was wrong" from "the encoder died".
#
# POLY_MS_RUNTIME moved the window and left the publisher at 150. Any value
# above that produced a run where the source stopped partway through and every
# platform spent the remainder with no input -- reported by all of them at once
# as an unstable connection, with the session going offline while the suite went
# on measuring. NOTHING FAILED AND NOTHING SAID SO: the assertions read bytes
# that had already arrived, so the run still passed and the only symptom was on
# somebody else's dashboard.
#
# The override exists precisely so a human can watch that dashboard -- see the
# note beside MS_RUNTIME -- so the case it broke is the case it was added for.
# Deriving the publisher keeps the headroom whatever window is asked for.
publish $(( MS_RUNTIME + 75 ))
sleep 6

# THE INGEST MUST REALLY CARRY TWO AUDIO TRACKS, and this is checked before any
# routing assertion is interpreted.
#
# Every per-destination check downstream is vacuous without it. A profile
# selecting track 1 on a single-track ingest compiles to something else or to
# nothing, and "youtube received different audio from twitch" stops being a
# statement about routing and becomes one about the publisher. If E-RTMP
# multitrack ever stops surviving the hop, this is the check that says so
# instead of twenty confusing ones downstream.
NTRACKS=""
for _ in $(seq 1 40); do
  NTRACKS=$(drive srctracks 2>&1 | tail -1)
  [ "$NTRACKS" = "2" ] && break
  sleep 1
done
if [ "$NTRACKS" = "2" ]; then
  ok "the ingest was probed with 2 audio tracks (E-RTMP multitrack survived the hop)"
else
  bad "the ingest carries $NTRACKS audio tracks, not 2; nothing below would mean anything"
  note "the publisher muxes two AAC tracks into one FLV; if this reads 1, either"
  note "the host FFmpeg no longer writes E-RTMP multitrack or rtmpserver stopped"
  note "reading it. See internal/rtmpserver/setup.go."
  tail -5 publisher.log 2>/dev/null | sed 's/^/          /'
  exit 1
fi

# ------------------------------------------------- 2. sinks, then destinations

step "2. Local sinks, then the fan-out"
if [ "$MODE" = dry ]; then
  note "DRY RUN: no platform credential is set, so all four destinations point at"
  note "local RTMP listeners. Nothing here contacts any platform."
else
  note "LIVE RUN: at least one platform credential is set. A platform with a key"
  note "publishes for real; each also gets a local twin carrying the same profile."
fi

# SINKS FIRST, ALWAYS, and before any destination exists. A destination created
# against a port nobody is listening on is refused, the supervisor respawns it,
# and the restart check further down then reports a platform fault that is
# really this suite's own start order.
#
# THE BIND IS RETRIED, AND A BIND THAT NEVER HAPPENS IS FATAL. Both halves were
# bought with a misdiagnosis. Two runs of this suite back to back leave the
# previous run's ACCEPTED connection on a sink port in TIME_WAIT, and FFmpeg's
# RTMP listener does not set SO_REUSEADDR, so the fresh bind is refused with
# EADDRINUSE for one 2MSL. The first version waited ten seconds, shrugged
# (`|| true`), and carried on -- and the run then reported
#
#   FAIL  kick is not delivering (state=reconnecting outTimeMs=0)
#   FAIL  kick restarted 5 times; the far end is dropping it
#
# which is a confident, specific and completely wrong accusation against the
# platform. Retrying rides out the TIME_WAIT; exiting when it still will not
# bind is what stops this suite ever blaming a far end for its own socket.
start_sink() { # start_sink <platform>
  local plat="$1" port i
  port="$(sink_port "$plat")"
  for i in 1 2 3 4 5 6; do
    sink "$plat"
    poly_wait_port_ready "$port" 20 >/dev/null 2>&1 && return 0
  done
  return 1
}
for p in $PLATFORMS; do
  start_sink "$p" && continue
  bad "no local sink could bind port $(sink_port "$p") for $p"
  note "Nothing below could be measured, and every per-destination check would"
  note "have blamed the far end. Most often the previous run's connection is"
  note "still in TIME_WAIT on that port; wait a minute and run it again."
  exit 1
done

# LIVE names the platforms that will really publish; TWIN names the local
# destination that carries the same profile so the received mix is readable.
LIVE=""
SKIPPED=""
for p in $PLATFORMS; do
  ke="$(key_env "$p")"; ue="$(url_env "$p")"
  if [ "$MODE" = dry ]; then
    # Every platform runs, locally, under the synthetic key.
    OUT=$(drive adddest "$p" "$p" "rtmp://127.0.0.1:$(sink_port "$p")/live" \
          POLY_DRY_STREAM_KEY "$(tracks_of "$p")")
    case "$OUT" in
      *DEST_OK*) LIVE="$LIVE $p" ;;
      *) bad "could not create the $p destination: $OUT" ;;
    esac
    continue
  fi
  if [ -z "${!ke:-}" ]; then
    SKIPPED="$SKIPPED $p"
    continue
  fi
  url="${!ue:-$(def_url "$p")}"
  if [ -z "$url" ]; then
    # A key with no endpoint. Reported as a skip naming the variable rather
    # than guessed at: Kick issues a per-channel ingest host and there is no
    # address this suite could invent that would be that channel's.
    SKIPPED="$SKIPPED $p"
    note "$p has a key but no ingest URL; set $ue"
    continue
  fi
  OUT=$(drive adddest "$p" "$p" "$url" "$ke" "$(tracks_of "$p")")
  case "$OUT" in
    *DEST_OK*) : ;;
    *) bad "could not create the $p destination: $OUT"; SKIPPED="$SKIPPED $p"; continue ;;
  esac
  # THE LOCAL TWIN. Same profile, local sink, so the mix this profile produces
  # is readable even though the platform's far end is not. It is evidence about
  # the PROFILE, not about the platform, and every line that reads it says so.
  OUT=$(drive adddest "$p-twin" custom "rtmp://127.0.0.1:$(sink_port "$p")/live" \
        POLY_DRY_STREAM_KEY "$(tracks_of "$p")")
  case "$OUT" in
    *DEST_OK*) LIVE="$LIVE $p" ;;
    *) bad "could not create the $p twin: $OUT"; SKIPPED="$SKIPPED $p" ;;
  esac
done

# THE LOOPBACK DESTINATION. Created alongside the four platform destinations,
# so it publishes through the same fan-out, in the same window, off the same
# ingest -- and its far end is this product's own RTMP server rather than an
# ffmpeg listener that accepts anything.
#
# NOT A PLATFORM AND NOT IN $PLATFORMS. Steps 3 to 6 iterate that list, and
# adding a fifth entry to it would change what every one of them measures; the
# loopback is a different question with its own step. It is created here rather
# than in 4b because what is being measured is the fan-out's own output, and a
# destination started twenty seconds after the others would be measured against
# a different moment of it.
#
# BOTH TRACKS, so the mix offered to the product's ingest is the widest one
# routing can build. Nothing downstream compares it with a platform's -- the
# question here is acceptance, not content.
LOOPDEST=loopback-ingest
LOOPBACK_UP=no
OUT=$(drive adddest "$LOOPDEST" custom "rtmp://127.0.0.1:$INGEST/live" \
      POLY_LOOPBACK_KEY 0,1)
case "$OUT" in
  *DEST_OK*) LOOPBACK_UP=yes ;;
  *) note "could not create the loopback destination: $OUT" ;;
esac

# recname is where a platform's readable mix lands: its own recording in a dry
# run, its twin's in a live one. One name so every measurement below is written
# once.
recname() { printf 'recv-%s.mkv\n' "$1"; }
# destname is the destination whose COMPILED routing speaks for this platform.
# In a live run that is the platform destination itself -- the twin exists to be
# heard, not to be read.
destname() { printf '%s\n' "$1"; }

for p in $PLATFORMS; do
  case " $LIVE " in *" $p "*) ;; *) continue ;; esac
  ok "destination created for $p (profile selects track(s) $(tracks_of "$p"))"
done
for p in $SKIPPED; do
  ke="$(key_env "$p")"
  sk "$p: no usable credential ($ke unset, or its ingest URL is unset) — NOT a pass"
done

# Let the fan-out run.
#
# TWENTY SECONDS IS RIGHT FOR LOCAL SINKS AND WRONG FOR A LIVE PLATFORM, so the
# window depends on which one is being measured.
#
# A local ffmpeg listener is delivering within a second of the connect, so 20s is
# comfortably past any startup transient and every check downstream has the data
# it needs. A real platform is a different system: it accepts the publish, then
# transcodes, then populates a preview, and 20s routinely ends the run before
# anything is visible to a human watching the dashboard. That is not a failure
# the suite can see -- every assertion here reads bytes and out_time, both of
# which are healthy long before a preview renders -- but it makes the run
# useless for the one thing a person does with it, which is look.
#
# So: 20s in dry mode, 75s in live mode, and an override for either. The
# publisher outlives the window in both cases; step 7 asserts it.
#
# KEYED ON $MODE, NOT ON $LIVE. $LIVE holds every destination that was created
# successfully, which in dry mode is all of them -- a first attempt keyed on it
# stretched the dry run to 75s and reported "live platforms configured" while
# contacting nothing. $MODE is the flag that actually distinguishes them.
note "measuring over ${MS_RUNTIME}s ($MODE mode)"
sleep "$MS_RUNTIME"

# ----------------------------------------------- 3. what routing compiled

step "3. Each destination compiled the tracks its own profile names"
for p in $PLATFORMS; do
  case " $LIVE " in
    *" $p "*)
      got=$(drive tracks "$(destname "$p")" 2>&1 | tail -1)
      want="$(tracks_of "$p")"
      if [ "$got" = "$want" ]; then
        ok "$p compiled to track(s) $got, which is what its profile names"
      else
        bad "$p compiled to track(s) '$got', but its profile names '$want'"
      fi ;;
    *) sk "$p: no destination, so nothing compiled — NOT a pass" ;;
  esac
done

step "4. Each destination is really delivering"
for p in $PLATFORMS; do
  case " $LIVE " in
    *" $p "*) : ;;
    *)
      sk "$p: no destination, so delivery is unmeasured — NOT a pass"
      sk "$p: no destination, so restarts are unmeasured — NOT a pass"
      continue ;;
  esac
  line=$(drive deststat "$(destname "$p")" 2>&1 | tail -1)
  set -- $line
  state="${1:-none}"; restarts="${2:--1}"; outms="${3:--1}"
  if [ "$state" = "running" ] && isnum "$outms" && [ "$outms" -gt 0 ]; then
    ok "$p is running and has produced ${outms}ms of media"
  else
    bad "$p is not delivering (state=$state outTimeMs=$outms)"
  fi
  # RESTARTS, not "is it up now". A platform that refuses a stream drops the
  # connection and the supervisor brings it straight back, so a destination that
  # has been refused four times reads as "running" at any instant you look.
  if isnum "$restarts" && [ "$restarts" -eq 0 ]; then
    ok "$p held one connection for the whole run (0 restarts)"
  elif [ "$restarts" = "-1" ]; then
    bad "$p has no process at all"
  else
    bad "$p restarted $restarts times; the far end is dropping it"
  fi
done

# ------------------------- 4b. the far end that is allowed to say no

step "4b. The loopback far end: polyemesis's own ingest accepted the publish"
# WHY THIS STEP EXISTS AND THE OTHER FOUR CANNOT REPLACE IT.
#
# Every sink in this suite is `ffmpeg -f flv -listen 1`. It accepts essentially
# any bytes on any playpath from anyone, so "the sink recorded something" proves
# the fan-out produced A STREAM and nothing at all about whether a real RTMP
# server would have taken it. internal/rtmpserver is a real one: it completes
# the handshake, resolves the publish path through Lookup(streamKey), and
# refuses a key it does not know. Publishing into it measures the one thing four
# permissive listeners cannot -- that the output is ingestible by something with
# the standing to refuse it.
#
# MEASURED BEFORE STEP 5'S stopall, because every fact here is about a LIVE
# session: `publishing` is the listener's answer about a publisher it currently
# holds, and after the stop it is correctly false.

# 4b-1. THE TWO SOURCES MUST NOT SHARE AN ADDRESS.
#
# The whole safety of a loopback rests on the destination landing in a DIFFERENT
# programme from the one feeding it. If both sources answered to one token, the
# fan-out would be publishing back into its own ingest -- the cycle this design
# exists to avoid -- and every check below would still pass, cheerfully, while
# the install ate itself. Asserted rather than assumed because it is a property
# of token minting, which is the product's to get right and this suite's to
# notice if it ever stops.
if [ -n "$LOOPKEY" ] && [ "$LOOPKEY" != "$PUBKEY" ]; then
  ok "the loopback source has its own publish token, distinct from the fan-out source's"
else
  bad "the loopback source's token is not distinct from the fan-out source's; a publish to it would feed back into the source it came from"
fi

# 4b-2 and 4b-5 come from ONE read, so they describe the same instant.
#
# Polled rather than sampled once: the destination has been running since step 2
# and should be long since accepted, but a machine under load can be a few
# seconds behind, and a single early sample would report a refusal the product
# never made.
LOOP_PUBLISHING=unknown; LOOP_BYTES=-1; LOOP_DESTS=-1; LOOP_DESTS_CLAIMED=-1
for _ in $(seq 1 30); do
  set -- $(drive srcstat "$LOOPSRC" 2>&1 | tail -1)
  LOOP_PUBLISHING="${1:-unknown}"; LOOP_BYTES="${2:--1}"
  LOOP_DESTS="${3:--1}"; LOOP_DESTS_CLAIMED="${4:--1}"
  [ "$LOOP_PUBLISHING" = true ] && isnum "$LOOP_BYTES" && [ "$LOOP_BYTES" -gt 0 ] && break
  sleep 1
done

# 4b-2. THE PRODUCT ACCEPTED ITS OWN OUTPUT.
#
# Two facts, and both are needed. `publishing` is rtmpserver's own answer about
# a session it is holding right now, so it says the handshake completed and the
# key resolved to this source. BytesIn says media actually flowed afterwards --
# a connection that is admitted and then sends nothing is a different and much
# quieter failure, and the two are told apart here rather than folded together.
if [ "$LOOP_PUBLISHING" = true ] && isnum "$LOOP_BYTES" && [ "$LOOP_BYTES" -gt 0 ]; then
  ok "polyemesis's own RTMP ingest ACCEPTED the loopback publish (${LOOP_BYTES} bytes in on the loopback source)"
else
  bad "polyemesis's own RTMP ingest did not accept the loopback publish (publishing=$LOOP_PUBLISHING bytesIn=$LOOP_BYTES)"
  note "the destination publishes to rtmp://127.0.0.1:$INGEST/live/<the loopback"
  note "source's token>. A refusal here is rtmpserver declining the key, the"
  note "source being not-ready, or the fan-out never having produced anything."
fi

# 4b-3. ACCEPTED ONCE, not accepted-on-the-fourth-try.
#
# Step 4 makes this argument for platforms and it is the same one here: a
# refused publish drops the connection, the supervisor respawns it immediately,
# and a destination that has been refused four times reads as "running" at any
# instant you look at it. Only the restart count can tell a session that was
# accepted from one that keeps being thrown out.
if [ "$LOOPBACK_UP" = yes ]; then
  set -- $(drive deststat "$LOOPDEST" 2>&1 | tail -1)
  lstate="${1:-none}"; lrestarts="${2:--1}"
  if isnum "$lrestarts" && [ "$lrestarts" -eq 0 ] && [ "$lstate" = running ]; then
    ok "the loopback destination held ONE connection to the ingest (0 restarts)"
  elif [ "$lrestarts" = "-1" ]; then
    bad "the loopback destination has no process at all (state=$lstate)"
  else
    bad "the loopback destination restarted $lrestarts times (state=$lstate); the ingest is refusing it and the supervisor keeps redialling"
  fi
else
  bad "the loopback destination was never created, so acceptance is unmeasured"
fi

# 4b-4. THE POSITIVE CONTROL, AND WITHOUT IT 4b-2 IS WORTH NOTHING.
#
# "The server accepted us" is not a measurement unless the server can refuse.
# The four ffmpeg sinks cannot, which is the entire reason this step exists, and
# a loopback check with no refusal half would pass identically against a server
# that had no key checking at all. So the same listener, at the same moment, is
# offered a key it has never issued and must throw it out.
#
# THE WRONG KEY IS FRESH RANDOM OF THE SAME LENGTH, never a mutation of the real
# one. A one-character edit of a live token spelled on an argv discloses all but
# one character of it to every user on the machine -- inside a check written to
# prove this suite does not do that. Same length so the refusal is about the key
# not being known, not about the path being the wrong shape.
WRONGKEY="$(od -An -tx1 -N48 /dev/urandom | tr -d ' \n' | cut -c1-"${#LOOPKEY}")"
rm -f wrongkey.rc
# DELIBERATELY TINY, and unlike the publisher above that is a decision rather
# than an oversight. This encoder exists to be REFUSED: it proves a wrong key is
# rejected, and a rejection arrives during the handshake, before a single frame
# of video matters. Sending 1080p60 at it would cost bandwidth to test nothing
# the handshake has not already settled.
#
# THE EXIT STATUS GOES TO A FILE, not to `wait`.
#
# bash reaps a background child asynchronously and remembers its status in a
# table of bounded size; with a publisher, four sinks and this all in flight,
# `wait $pid` on an already-reaped job can answer 127 -- "no such job" -- which
# is non-zero and would be read here as a refusal. A check whose PASS can be
# produced by its own bookkeeping is not a check. The subshell writes the real
# status where nothing else can invent it.
( ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=320x180:rate=15" \
    -f lavfi -i "sine=frequency=$TONE_A:sample_rate=48000" \
    -metadata comment=multistream-wrongkey \
    -map 0:v -map 1:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 30 -pix_fmt yuv420p -b:v 300k \
    -c:a aac -b:a 64k -ac 2 -t 5 \
    -f flv "rtmp://127.0.0.1:$INGEST/live/$WRONGKEY" > wrongkey.log 2>&1
  printf '%s\n' "$?" > wrongkey.rc ) &
# BOUNDED, and the bound is a verdict rather than a shrug. An encoder still
# alive after 30s was neither accepted nor refused, which is its own finding --
# rtmpserver's handshakeTimeout is 10s and an unknown key is answered
# immediately, so nothing legitimate takes this long. Waiting forever is issue
# #179; carrying on regardless is what scripts/termination-guard.sh refuses.
wrongrc=timeout
for _ in $(seq 1 60); do
  [ -s wrongkey.rc ] && { wrongrc="$(cat wrongkey.rc)"; break; }
  sleep 0.5
done
if [ "$wrongrc" = timeout ]; then
  # OBSERVED, not requested. scripts/termination-guard.sh refuses a kill followed
  # by a verdict, and it is right to here: this branch is about a prober that
  # would not resolve, so rendering the finding while it may still be publishing
  # leaves an encoder holding the ingest through everything below.
  pkill -9 -f "multistream-wrongkey" 2>/dev/null
  wrongdead=no
  for _ in $(seq 1 20); do
    pgrep -f "multistream-wrongkey" >/dev/null 2>&1 || { wrongdead=yes; break; }
    sleep 0.25
  done
  [ "$wrongdead" = yes ] || note "the unknown-key prober outlived SIGKILL; it may still hold the ingest"
  bad "a publish with an UNKNOWN stream key was neither accepted nor refused in 30s; 4b-2's 'accepted' proves nothing while this hangs"
elif [ "$wrongrc" -ne 0 ]; then
  ok "a publish with an UNKNOWN stream key was REFUSED by the same listener (ffmpeg exit $wrongrc), so 'accepted' above means something"
else
  bad "a publish with an UNKNOWN stream key SUCCEEDED; the ingest accepts keys it never issued, and every acceptance check here is vacuous"
  tail -3 wrongkey.log 2>/dev/null | sed 's/^/          /'
fi

# 4b-5. NO CYCLE. Read out of the same srcstat as 4b-2.
#
# A destination on the loopback source would publish that programme somewhere,
# and the one place it could plausibly be pointed is back at the fan-out's own
# ingest -- at which point each lap through the graph is a full decode, mix and
# encode, and the install climbs to saturation with every card green. Nothing in
# this suite creates one; the assertion is here so that a later change which
# does is caught by a check rather than by a fan.
#
# COUNTED OFF GET /destinations, NOT read off the source view. The view's own
# `destinations` field is always 0 -- see srcStat in the driver -- so asserting
# on it would have made this check pass for a source with any number of
# destinations, which is the exact state it exists to catch. The mutation that
# found that is on the PR.
if isnum "$LOOP_DESTS" && [ "$LOOP_DESTS" -eq 0 ]; then
  ok "the loopback source fans out to NOTHING (0 destinations), so the graph cannot cycle"
else
  bad "the loopback source carries $LOOP_DESTS destination(s); the fan-out can feed back into itself and amplify"
fi
# A FINDING, REPORTED RATHER THAN JUDGED HERE, in step 8c's tradition.
#
# GET /sources renders `destinations` (and `renditions` beside it) for every
# source, documented in api.sourceView as what deleting that source would take
# with it -- the number a delete confirmation quotes -- and api.viewSource never
# populates either. So the confirmation says 0 however much the delete would
# really remove.
#
# MEASURED ON THE FAN-OUT SOURCE, not on the loopback one. The loopback source
# genuinely owns nothing, so 0 == 0 there and the fault is invisible; the
# fan-out source owns every destination this suite created, so the disagreement
# is real on every run. That is the difference between a finding that is
# reported and one that only appears when something else is already wrong.
#
# db.DefaultSourceName, spelled out because the driver addresses sources by
# name. A rename makes the driver report no such source, the fields come back
# non-numeric, and isnum below suppresses this quietly -- which is the right
# failure for a line that reports rather than judges. Nothing is counted here.
set -- $(drive srcstat Main 2>&1 | tail -1)
FANOUT_DESTS="${3:--1}"; FANOUT_CLAIMED="${4:--1}"
if isnum "$FANOUT_DESTS" && isnum "$FANOUT_CLAIMED" && [ "$FANOUT_DESTS" -ge 0 ] &&
   [ "$FANOUT_CLAIMED" -ne "$FANOUT_DESTS" ]; then
  printf "  \033[33mFINDING\033[0m  GET /sources claims %s destination(s) for the fan-out source; it owns %s\n" \
    "$FANOUT_CLAIMED" "$FANOUT_DESTS"
  note "api.sourceView.Destinations is declared and never assigned, so a delete"
  note "confirmation quotes 0 whatever it would really remove. Not a fan-out"
  note "fault and not counted; 4b-5 counts off GET /destinations because of it."
fi

# ------------------------------------------------- 5. what actually arrived

step "5. Each far end received the mix its profile names"
# STOP FIRST. An MKV muxer buffers, and a destination that is running perfectly
# can show almost nothing on disk until its output is closed; the sink cannot
# finalise its file until the publisher on the other end goes away.
drive stopall >/dev/null 2>&1
# Bounded wait for the sinks to notice EOF and finalise. Never unbounded: a sink
# that hangs would take the whole suite's deadline and report nothing.
for _ in $(seq 1 40); do
  pgrep -f "multistream-sink" >/dev/null 2>&1 || break
  sleep 0.5
done
# THE SIGNAL IS A REQUEST, NOT AN OUTCOME. `pkill` then `sleep 1` asks the sinks
# to die and then proceeds as though they had -- and a sink still holding its
# output open is a file the measurements below read half-written. That shape is
# what scripts/termination-guard.sh exists to refuse, and it caught this here.
#
# Bounded, because an unbounded wait on a process that will not die takes the
# whole suite's deadline and reports nothing -- issue #179 exactly.
pkill -f "multistream-sink" 2>/dev/null
dead=no
for _ in $(seq 1 40); do
  pgrep -f "multistream-sink" >/dev/null 2>&1 || { dead=yes; break; }
  sleep 0.25
done
if [ "$dead" != yes ]; then
  # SIGKILL, then observe that too. A sink that ignored SIGTERM is still holding
  # the file the checks below are about.
  pkill -9 -f "multistream-sink" 2>/dev/null
  for _ in $(seq 1 20); do
    pgrep -f "multistream-sink" >/dev/null 2>&1 || { dead=yes; break; }
    sleep 0.25
  done
fi
[ "$dead" = yes ] || bad "a multistream sink outlived SIGKILL; the recordings below may be half-written"

# Measured once, into named variables, so the cross-destination comparisons in
# step 6 read the same numbers these checks did.
for p in $PLATFORMS; do
  f="$(recname "$p")"
  va="$(rms "$f" "$TONE_A" "$BAND_A")"
  vb="$(rms "$f" "$TONE_B" "$BAND_B")"
  # An assignment's right-hand side is not word-split, so eval here cannot
  # re-parse a measurement that came back as an ffmpeg error string.
  eval "A_$p=\$va"
  eval "B_$p=\$vb"
done
band_a() { eval "printf '%s\n' \"\${A_$1}\""; }
band_b() { eval "printf '%s\n' \"\${B_$1}\""; }

for p in $PLATFORMS; do
  case " $LIVE " in
    *" $p "*) : ;;
    *)
      sk "$p: nothing was published, so nothing was received — NOT a pass"
      sk "$p: nothing was published, so exclusion is unmeasured — NOT a pass"
      continue ;;
  esac
  a="$(band_a "$p")"; b="$(band_b "$p")"
  via=""
  [ "$MODE" = live ] && via=" (measured on the local twin of this profile; what the platform itself received is not observable from here)"
  if ! isnum "$a" || ! isnum "$b"; then
    bad "$p: could not measure $(recname "$p") (bands read '$a' / '$b')"
    bad "$p: exclusion unmeasurable for the same reason"
    note "sink log: $(tail -2 "sink-$p.log" 2>/dev/null | tr '\n' ' ')"
    continue
  fi
  case "$(tracks_of "$p")" in
    0)
      if gt "$a" "$TONE_FLOOR"; then
        ok "$p received track 0's ${TONE_A}Hz tone at ${a}dB$via"
      else
        bad "$p should carry track 0's ${TONE_A}Hz tone; measured ${a}dB"
      fi
      if gt "$a" "$(plus "$b" "$TONE_MARGIN")"; then
        ok "$p did NOT receive track 1 (${TONE_B}Hz is ${b}dB, ${TONE_MARGIN}dB+ below)"
      else
        bad "$p carries track 1's ${TONE_B}Hz at ${b}dB against ${a}dB — it received a mix its profile does not name"
      fi ;;
    1)
      if gt "$b" "$TONE_FLOOR"; then
        ok "$p received track 1's ${TONE_B}Hz tone at ${b}dB$via"
      else
        bad "$p should carry track 1's ${TONE_B}Hz tone; measured ${b}dB"
      fi
      if gt "$b" "$(plus "$a" "$TONE_MARGIN")"; then
        ok "$p did NOT receive track 0 (${TONE_A}Hz is ${a}dB, ${TONE_MARGIN}dB+ below)"
      else
        bad "$p carries track 0's ${TONE_A}Hz at ${a}dB against ${b}dB — it received a mix its profile does not name"
      fi ;;
    0,1)
      if gt "$a" "$TONE_FLOOR" && gt "$b" "$TONE_FLOOR"; then
        ok "$p received BOTH tracks (${TONE_A}Hz ${a}dB, ${TONE_B}Hz ${b}dB)$via"
      else
        bad "$p should carry both tracks; measured ${TONE_A}Hz ${a}dB, ${TONE_B}Hz ${b}dB"
      fi
      # Both present AND balanced. Presence alone would pass on one real track
      # plus the other's bleed through the mixer, which is the same audible
      # failure wearing a passing measurement.
      d="$(absdiff "$a" "$b")"
      if gt "$TONE_MATCH" "$d"; then
        ok "$p's two tracks are within ${d}dB of each other, so both are really in the mix"
      else
        bad "$p's tracks differ by ${d}dB; one of them is bleed, not a mix"
      fi ;;
  esac
done

# ------------------------------------ 6. and the far ends differ from each other

step "6. Two profiles, two mixes — and one shared profile, one mix"
# THE CHECK THE WHOLE SUITE IS FOR.
#
# Every check above is per destination, and every one of them would still pass
# if routing sent the SAME mix everywhere -- provided that mix happened to be
# the one the destination under test expected. Only a comparison BETWEEN
# destinations can see it. Balance, not raw level: the difference of the two
# bands within one recording, which is immune to the recordings having different
# gains or lengths.
balance() { # balance <platform> -> bandA - bandB, in dB
  local a b; a="$(band_a "$1")"; b="$(band_b "$1")"
  isnum "$a" && isnum "$b" || { printf ''; return; }
  awk -v x="$a" -v y="$b" 'BEGIN{print x-y}'
}
have() { case " $LIVE " in *" $1 "*) return 0 ;; *) return 1 ;; esac }

if have twitch && have youtube; then
  bt="$(balance twitch)"; by="$(balance youtube)"
  if isnum "$bt" && isnum "$by" && gt "$(absdiff "$bt" "$by")" "$PROFILE_SPREAD"; then
    ok "twitch and youtube received DIFFERENT audio (balance ${bt}dB vs ${by}dB)"
  else
    bad "twitch and youtube received the same mix (balance ${bt}dB vs ${by}dB) — per-destination routing collapsed"
  fi
else
  sk "twitch/youtube difference needs both credentials — NOT a pass"
fi

if have twitch && have facebook; then
  bt="$(balance twitch)"; bf="$(balance facebook)"
  if isnum "$bt" && isnum "$bf" && gt "$TONE_MATCH" "$(absdiff "$bt" "$bf")"; then
    ok "twitch and facebook share a profile and received the SAME audio (${bt}dB vs ${bf}dB)"
  else
    bad "twitch and facebook share a profile but received different audio (${bt}dB vs ${bf}dB)"
  fi
else
  sk "twitch/facebook sameness needs both credentials — NOT a pass"
fi

# The compiled graphs, as a second and independent witness. The recordings say
# what arrived; these say what the engine decided to send, and a disagreement
# between the two witnesses is itself informative.
if have twitch && have youtube; then
  gt_="$(drive graph "$(destname twitch)" 2>&1 | tail -1)"
  gy_="$(drive graph "$(destname youtube)" 2>&1 | tail -1)"
  if [ -n "$gt_" ] && [ "$gt_" != "$gy_" ]; then
    ok "twitch and youtube compiled DIFFERENT filtergraphs"
  else
    bad "twitch and youtube compiled the same filtergraph: $gt_"
  fi
else
  sk "filtergraph difference needs both credentials — NOT a pass"
fi

if have twitch && have facebook; then
  gt_="$(drive graph "$(destname twitch)" 2>&1 | tail -1)"
  gf_="$(drive graph "$(destname facebook)" 2>&1 | tail -1)"
  if [ -n "$gt_" ] && [ "$gt_" = "$gf_" ]; then
    ok "twitch and facebook compiled the SAME filtergraph"
  else
    bad "twitch and facebook share a profile but compiled different graphs: '$gt_' vs '$gf_'"
  fi
else
  sk "filtergraph sameness needs both credentials — NOT a pass"
fi

# ------------------------------------------------------------ 7. the publisher

step "7. The encoder outlived the measurement"
# A publisher that died mid-run would make every "did not receive the other
# track" check above pass for the wrong reason: silence is absent from every
# band. Asserted at the END, not the start.
if pgrep -f "multistream-publisher" >/dev/null 2>&1; then
  ok "the publisher was still running when the measurements were taken"
else
  bad "the publisher exited before the run finished; the silence checks above prove nothing"
  tail -5 publisher.log 2>/dev/null | sed 's/^/          /'
fi

# --------------------------------------------------------- 8. the credentials

step "8. No credential reached a log, an artifact or a command line"
# THE VALUES THIS STEP LOOKS FOR are whatever was actually configured: the
# synthetic dry key always, plus every real key that was set. Building the list
# from the environment rather than from a constant is what keeps a live run
# guarded as tightly as a dry one.
KEY_ENVS="POLY_DRY_STREAM_KEY"
for p in $PLATFORMS; do
  ke="$(key_env "$p")"
  [ -n "${!ke:-}" ] && KEY_ENVS="$KEY_ENVS $ke"
done

# 8a. Every read-reachable API rendering. This is #150's egress list, driven
# against a server that really published: /destinations, /status, /processes,
# /processes/{name}/logs and /settings. internal/api/argv_leak_test.go is the
# unit-level guard for the same class; this is it against a live fan-out.
OUT=$(drive leakscan $KEY_ENVS 2>&1 | tail -5)
if [ "$OUT" = "SAFE" ]; then
  ok "no configured key appears in any read-reachable API rendering"
else
  bad "a configured key is readable through the API: $OUT"
fi

# 8b. The working directory, which is server.log, every publisher and sink log,
# and every recording.
#
# THE PATTERN IS A DISTINCTIVE PREFIX OF EACH KEY, NOT THE WHOLE VALUE, and that
# is #307 -- the defect in the INSTRUMENT rather than in the product.
#
# This check used to search for the exact configured bytes. It PASSED on a live
# run while a stream key sat in data/logs/process.log, and so did 8c, because
# 8c's positive control searched the same exact bytes in a place that did hold
# them. Measured on that run:
#
#   exact env value present in log : False
#   key PREFIX (first 20 chars)    : True
#
# The configured value carried a trailing bracketed-paste artefact, so what
# reached the wire and the log was the value WITHOUT it -- see #306. That
# specific cause is incidental and is now refused at the API boundary. The
# general one is not: URL-encoding, truncation at a delimiter, a wrapped or
# split line, a key embedded in a longer token, all defeat an exact search
# equally. The question this step is here to answer is "is a recognisable
# credential present", and only a prefix asks it.
#
# 16 CHARACTERS. Far past coincidence for these formats -- a 16-character run of
# a platform's key alphabet does not occur by accident in an FFmpeg log or an
# MPEG-TS recording -- and short enough to survive the transformations above,
# which take bytes off the END. A key shorter than this is searched whole, which
# is the old behaviour and is stated below rather than silently applied.
#
# THE PATTERN IS STILL PASSED ON A FILE DESCRIPTOR, NOT AN ARGUMENT.
# `grep -F "$KEY"` would put the credential on grep's own argv and disclose it
# to every user on the machine -- inside the check written to prove that never
# happens. Process substitution gives grep /dev/fd/N backed by a pipe: nothing
# on a command line, nothing on disk. Everything below that touches a key value
# does so through shell builtins (parameter expansion, printf), which fork
# nothing and therefore expose nothing.
#
# The state database is excluded BY DESIGN and audited separately in 8c. It is
# where the key is meant to live; a destination whose credential the server had
# forgotten could not publish at all.
KEY_PREFIX_LEN=16
# key_pattern writes the bytes 8b searches for to stdout, given a key value.
#
# ONE definition, called by 8b and by 8b' below, so the control exercises the
# predicate under test rather than a second copy of it that can drift away from
# it silently. Its argument is a shell function's positional parameter: no fork,
# no exec, so the value reaches no process's argv.
key_pattern() { printf '%s\n' "${1:0:$KEY_PREFIX_LEN}"; }
leaked=""
short=""
for e in $KEY_ENVS; do
  v="${!e}"
  [ -n "$v" ] || continue
  [ "${#v}" -lt "$KEY_PREFIX_LEN" ] && short="$short $e"
  if grep -rIl -F -f <(key_pattern "$v") . \
       --exclude=polyemesis.db --exclude=polyemesis.db-wal --exclude=polyemesis.db-shm \
       2>/dev/null | grep -q .; then
    leaked="$leaked $e"
  fi
done
if [ -z "$leaked" ]; then
  ok "no recognisable prefix of any configured key appears in a log, recording or artifact"
else
  bad "a key was written to the working directory:$leaked"
  for e in $leaked; do
    v="${!e}"
    note "$e found in: $(grep -rIl -F -f <(key_pattern "$v") . '--exclude=polyemesis.db*' 2>/dev/null | tr '\n' ' ')"
  done
fi
[ -n "$short" ] && note "searched whole (shorter than $KEY_PREFIX_LEN chars):$short"

# 8b'. THE CONTROL FOR 8b's PREDICATE, which is a different question from 8c's.
#
# 8c proves the MECHANISM works -- the file set, the encoding and the grep --
# by finding a destination name this suite wrote. It cannot prove the PREDICATE
# is the right one, and on the run behind #307 the mechanism was correct and the
# predicate was wrong, so both the check and its control agreed on a false clean.
#
# This plants exactly the case the old predicate missed: a TRUNCATED copy of
# each key, which is what a paste artefact produced in the real log, and then
# runs both predicates over it. The prefix search must find it and the exact
# search must NOT -- the second half is the whole point, because it is the
# measurement that says the new predicate catches something the old one let
# through rather than merely agreeing with it.
#
# Written with printf into a file under the work dir, and removed the moment the
# verdict is in. It is planted AFTER 8b has already scanned, so it cannot be
# what 8b found.
PROBE_DIR="$WORK/leakcheck-predicate-probe"
rm -rf "$PROBE_DIR"; mkdir -p "$PROBE_DIR"
probe_found=""; probe_missed=""; probe_exact=""; probe_skipped=""
for e in $KEY_ENVS; do
  v="${!e}"
  # A truncated copy has to be shorter than the value and longer than the
  # prefix, or it is not the case under test. 8 characters off the end, so the
  # value needs KEY_PREFIX_LEN+8 to spare.
  if [ "${#v}" -lt "$((KEY_PREFIX_LEN + 8))" ]; then
    probe_skipped="$probe_skipped $e"
    continue
  fi
  printf '%s\n' "dest:9: Error opening output file rtmp://ingest.invalid/${v:0:$((${#v} - 8))}" \
    > "$PROBE_DIR/$e.log"
  if grep -q -F -f <(key_pattern "$v") "$PROBE_DIR/$e.log" 2>/dev/null; then
    probe_found="$probe_found $e"
  else
    probe_missed="$probe_missed $e"
  fi
  if grep -q -F -f <(printf '%s\n' "$v") "$PROBE_DIR/$e.log" 2>/dev/null; then
    probe_exact="$probe_exact $e"
  fi
done
if [ -n "$probe_missed" ]; then
  bad "8b's predicate does not find a truncated key it was planted:$probe_missed"
  note "8b's 'nothing found' means nothing while this fails."
elif [ -n "$probe_exact" ]; then
  bad "the OLD exact-match predicate also finds the planted truncation:$probe_exact"
  note "the probe does not reproduce #307, so the new predicate is not shown to"
  note "catch anything the old one missed. Fix the probe, not the check."
elif [ -z "$probe_found" ]; then
  bad "no key was long enough to probe with:$probe_skipped"
else
  ok "8b's predicate finds a truncated key that an exact search does not"
  note "planted$probe_found, each as the configured value minus its last 8"
  note "characters -- the shape that certified clean on the run behind #307."
  [ -n "$probe_skipped" ] && note "too short to probe with:$probe_skipped"
fi
rm -rf "$PROBE_DIR"

# 8c. THE POSITIVE CONTROL FOR 8b, and without it 8b is worth very little.
#
# "We searched and found nothing" and "our search does not work" produce the
# same green line. The one file that MUST contain the key is the state database
# -- a destination whose credential the server had forgotten could not publish
# at all -- so finding it exactly there, and nowhere else, is what turns 8b from
# an absence into a measurement. This is the shape internal/testenv's skip
# census argues for: a check that cannot fail is not a check.
DBF="$DATA/polyemesis.db"
# The WAL sidecar is searched too, WHEN IT EXISTS. The database runs in
# journal_mode(WAL), so a row written since the last checkpoint lives in
# polyemesis.db-wal and not in polyemesis.db; a control that looked only at the
# main file would fail on timing and be read as a broken search.
CTLFILES="$DBF"
[ -f "$DBF-wal" ] && CTLFILES="$CTLFILES $DBF-wal"
# grep OVER THE FILES, never `cat ... | grep -q`. Under `set -o pipefail` a
# `grep -q` that MATCHES exits at once, cat takes SIGPIPE, and the pipeline
# reports 141 -- so the check fails exactly when it should pass. That cost this
# suite a run; acceptance-docker.sh carries the same note about `docker logs`.
if [ ! -f "$DBF" ]; then
  bad "no state database at $DBF; 8b searched an install that stored nothing"
# THE ANCHOR IS A DESTINATION NAME, NOT THE KEY, and it changed because the
# product changed underneath this control.
#
# It used to assert the KEY was findable here, on the reasoning that 8b's "found
# nowhere" is worthless until the search is shown to find something. That
# reasoning is right and the anchor was wrong: destination stream keys are now
# sealed at rest with NaCl secretbox, so the plaintext key is DELIBERATELY absent
# from this file. Run against a server carrying that change, this control failed
# and correctly refused to let 8b be trusted -- it could not tell "the search is
# broken" from "the product stopped storing this in the clear".
#
# A destination NAME is stored in the clear, and this suite knows what it wrote,
# so it proves the file set, the encoding and the grep all work. Only then does
# the key's absence mean what 8b needs it to mean.
elif ! grep -q -F -f <(printf '%s\n' "twitch") $CTLFILES 2>/dev/null; then
  bad "a destination name this suite created is not in the state database"
  note "8b's 'no key found' cannot be trusted while this fails: the file set,"
  note "the encoding or the grep is wrong, not the product."
elif grep -q -F -f <(printf '%s\n' "$POLY_DRY_STREAM_KEY") $CTLFILES 2>/dev/null; then
  # A pass: the search demonstrably works, which is all 8b needs from it. That
  # the key is also READABLE here is a separate finding and gets its own line
  # rather than being folded into a control's verdict.
  ok "8b's search works -- a destination name is findable in the state database"
  note "FINDING: the stream key is ALSO present in plaintext, so this install is"
  note "not sealing destination keys. Expected only on a build predating that."
else
  ok "8b's search works, and the stream key is not in the state database at all"
  note "the key is sealed at rest, and a name this suite wrote IS findable in the"
  note "same files -- so the absence is the product's doing, not the search's."
fi

# A FINDING, REPORTED RATHER THAN JUDGED HERE.
#
# FIXED SINCE THIS WAS WRITTEN, and the note is kept because the reasoning is
# what mattered. This suite's positive control read the state database to prove
# its own search worked, and in doing so inventoried where the secret actually
# lived: mode 0644 inside a 0755 data directory, with no UMask in
# deploy/polyemesis.service -- every platform credential, the admin bcrypt hash
# and the session secrets readable by every account on the host.
#
# That became issue #297. The database, its -wal and -shm sidecars and the data
# directory are now narrowed at db.Open and in both deployment paths, and the
# keys themselves are sealed with NaCl secretbox rather than stored in the clear.
# What remains true, and is the reason this paragraph survives: a control that
# keeps a negative honest also tells you where the secret really is.
#
# It is NOT this suite's verdict. acceptance-failover.sh set the precedent when
# it found the relative --data fault: a suite does not get to change what it
# measures in order to pass, and it does not get to fail the product on a
# question it was not written to ask. This suite asks whether the FAN-OUT leaks
# a credential to a log, an artifact or a command line. Where the store keeps it
# is a different question with a different fix (internal/fsperm.SecureFile at
# db.Open, plus the WAL sidecar), a different blast radius and a different
# review. Filed as issue #297. Printed every run, loudly, so nobody has to
# rediscover it.
if [ -f "$DBF" ]; then
  perm=$(ls -l "$DBF" | cut -c1-10)
  case "$perm" in
    -rw-------*) : ;;
    *)
      printf "  \033[33mFINDING\033[0m  the state database is %s\n" "$perm"
      note "It holds every stream key in plaintext (8c just proved that). Not"
      note "counted as a failure of the fan-out; filed as issue #297." ;;
  esac
fi

# 8d. Command lines. THE HARNESS'S OWN PROCESSES must never carry a key.
#
# The comparison is done with bash's own pattern matching rather than by handing
# grep the value, for 8b's reason: a check that discloses the secret while
# looking for it is worse than no check.
#
# WHAT THIS DOES NOT CLAIM. The server's destination children DO carry the
# publish URL, key included, because RTMP has no other way to say where to
# publish -- see engine.destArgs and db.Destination.Target. The product's answer
# to that is engine.SecretSet.Scrub, which removes it from everything rendered
# back out, and 8a is the measurement of whether that works. What is checked
# here is that this suite adds no second copy of the exposure.
argv_leak=""
while IFS= read -r line; do
  case "$line" in
    *multistream-publisher*|*multistream-sink*) ;;
    *) continue ;;
  esac
  for e in $KEY_ENVS; do
    v="${!e}"
    [ -n "$v" ] || continue
    case "$line" in *"$v"*) argv_leak="$argv_leak $e" ;; esac
  done
done < <(ps -A -ww -o args= 2>/dev/null)
if [ -z "$argv_leak" ]; then
  ok "no process this suite spawned carries a key on its command line"
else
  bad "this suite put a key on a command line:$argv_leak"
fi

# ------------------------------------------------------------------ verdict

printf "\n\033[1m%d passed, %d failed, %d skipped\033[0m\n" "$pass" "$fail" "$skip"
if [ "$MODE" = dry ]; then
  printf "\033[33mDRY RUN — no platform was contacted. This proves the routing, the\n"
  printf "fan-out and this harness against local RTMP listeners. It says NOTHING\n"
  printf "about Twitch, YouTube, Kick or Facebook ingest.\033[0m\n"
else
  printf "\033[33mLIVE RUN — platforms with a key were published to for real. The\n"
  printf "received-audio checks were measured on local twins of each profile; what\n"
  printf "a platform itself decoded is not observable from here.\033[0m\n"
fi

# FIXED-VALUE GUARD. Confirm the COUNT, not just the verdict.
#
# Most of this suite sits behind "if this platform has a credential" branches, so
# a run that fell over early still reaches this line and prints "0 failed", which
# reads as success. Skips count toward the total because a skip is a deliberate,
# reported decision; a check that never ran is not. The postprod suite once
# reported "7 passed, 0 failed -- PASSED" having silently skipped five checks.
#
#   1. server started, setup, ingest, publish token, 2 audio tracks        5
#   2. one destination created per platform                                4
#   3. compiled track selection per platform                               4
#   4. delivering + restarts, per platform                                 8
#   4b. loopback: distinct token, the ingest accepted it, one connection,
#      a wrong key refused, and no destination on the loopback source      5
#   5. selected tone present + excluded tone absent, per platform          8
#   6. twitch/youtube differ, twitch/facebook match, and both again on
#      the compiled graphs                                                 4
#   7. the publisher outlived the run                                      1
#   8. api leak, workdir leak, the workdir predicate's own control, the
#      search's positive control, argv leak                                5
#
# 4b IS UNCONDITIONAL: it needs no platform credential, so it contributes the
# same five in a dry run and in a live one. That is deliberate -- the one part
# of this suite that measures a real RTMP server's acceptance must not be the
# part that disappears when nobody has a key.
# 44 since #307 added 8b', the control that proves 8b's PREDICATE rather than
# its mechanism. It is unconditional: POLY_DRY_STREAM_KEY is generated every run
# at 32 characters, so there is always a key long enough to plant a truncation
# of, in a dry run and in a live one alike.
EXPECTED_CHECKS=44
total=$((pass + fail + skip))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  only %d of %d checks ran; the run stopped early\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$total" -gt "$EXPECTED_CHECKS" ]; then
  printf "  \033[33mNOTE\033[0m  %d checks ran, %d expected. If checks were added,\n" \
    "$total" "$EXPECTED_CHECKS"
  printf "        raise EXPECTED_CHECKS so the guard keeps its teeth.\n"
fi
[ "$fail" -eq 0 ]
