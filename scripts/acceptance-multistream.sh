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

DRIVER="$WORK/multistream-driver"
go build -o "$DRIVER" "$SCRIPTS/acceptance_multistream_driver.go" || {
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
publish() { # publish <seconds>
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=$TONE_A:sample_rate=48000" \
    -f lavfi -i "sine=frequency=$TONE_B:sample_rate=48000" \
    -metadata comment=multistream-publisher \
    -map 0:v -map 1:a -map 2:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v 1200k \
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

poly_wait_port_ready "$INGEST" 15 || true
# 150s: long enough for every destination to run its measurement window, be
# stopped, and for the sinks to finalise, with the publisher still alive at the
# end so step 9 can tell "the routing was wrong" from "the encoder died".
publish 150
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

# Let the fan-out run. 20s is comfortably longer than any startup transient and
# leaves the publisher alive afterwards.
sleep 20

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
# THE PATTERN IS PASSED ON A FILE DESCRIPTOR, NOT AN ARGUMENT. `grep -F "$KEY"`
# would put the credential on grep's own argv and disclose it to every user on
# the machine -- inside the check written to prove that never happens. Process
# substitution gives grep /dev/fd/N backed by a pipe: nothing on a command line,
# nothing on disk.
#
# The state database is excluded BY DESIGN and audited separately in 8c. It is
# where the key is meant to live; a destination whose credential the server had
# forgotten could not publish at all.
leaked=""
for e in $KEY_ENVS; do
  v="${!e}"
  if grep -rIl -F -f <(printf '%s\n' "$v") . \
       --exclude=polyemesis.db --exclude=polyemesis.db-wal --exclude=polyemesis.db-shm \
       2>/dev/null | grep -q .; then
    leaked="$leaked $e"
  fi
done
if [ -z "$leaked" ]; then
  ok "no configured key appears in any log, recording or artifact under the work dir"
else
  bad "a key was written to the working directory:$leaked"
  for e in $leaked; do
    note "$e found in: $(grep -rIl -F -f <(printf '%s\n' "${!e}") . '--exclude=polyemesis.db*' 2>/dev/null | tr '\n' ' ')"
  done
fi

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
elif grep -q -F -f <(printf '%s\n' "$POLY_DRY_STREAM_KEY") $CTLFILES 2>/dev/null; then
  ok "the key is in the state database and in no other file, so 8b's search works"
else
  bad "the key is not in the state database either; 8b's search finds nothing anywhere"
  note "8b's 'no key found' result cannot be trusted while this fails -- the"
  note "pattern, the encoding or the file set is wrong, not the product."
fi

# A FINDING, REPORTED RATHER THAN JUDGED HERE.
#
# The database that 8c just confirmed holds every destination's stream key in
# plaintext is created at SQLite's default mode. On this run that is 0644 inside
# a 0755 data directory, and deploy/polyemesis.service sets no UMask, so a
# packaged install puts every platform credential, the admin bcrypt hash and the
# session secrets in a file every account on the host can read.
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
#   5. selected tone present + excluded tone absent, per platform          8
#   6. twitch/youtube differ, twitch/facebook match, and both again on
#      the compiled graphs                                                 4
#   7. the publisher outlived the run                                      1
#   8. api leak, workdir leak, the search's positive control, argv leak    4
EXPECTED_CHECKS=38
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
