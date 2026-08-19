#!/usr/bin/env bash
# Failover acceptance: a destination rides a source switch without noticing.
#
# The source-selector tier's whole promise is that the hub a destination reads
# never changes -- only the bytes arriving on it do. Switching a destination's
# own subscription would restart its process and drop the platform connection,
# which is the exact failure failover exists to prevent.
#
# The engine's design notes name the two things that decide whether that works,
# and BOTH were covered only against fakes:
#
#   PTS CONTINUITY. Each feed normalises its input to a timeline starting at
#   zero, so without -output_ts_offset every switch hands the destinations a
#   timestamp that jumps BACKWARDS by however long the last feed ran. A platform
#   answers a backwards jump by dropping the connection. Nothing in a unit test
#   produces a real timestamp, so nothing in a unit test can catch this.
#
#   RIDING THE SWITCH. The destination process must not restart. "The file has
#   bytes in it" would pass just as happily on a destination that died and came
#   back, so this suite counts restarts rather than inspecting output alone.
#
# Both are measured here against real FFmpeg, real timestamps and a real
# publisher that is killed and brought back.
#
# Usage:  ./scripts/acceptance-failover.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-failover}"
PORT=8096
# RTMP, not SRT: the host FFmpeg (Homebrew) is built without libsrt, so it can
# neither listen nor publish on SRT. SRT ingest is covered by the container
# suites. What this suite measures -- the timeline across a feed swap -- is
# the same either way.
INGEST=1938
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
# A deadline of our own. See lib-watchdog.sh: the job ceiling cancels a hung
# suite and prints nothing, so the suite has to give up first and say what it
# was waiting for.
. "$SCRIPTS/lib-watchdog.sh"
# Shared observation. See lib-observe.sh: a wait that gives up has to report
# what it saw, or a primary that arrived 200ms late reads exactly like one that
# never arrived. Issue #38.
. "$SCRIPTS/lib-observe.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

# BUILD IT. This suite used to run whatever binary happened to be sitting in the
# repo root, which meant a local run could pass against code from hours earlier
# while CI -- which always builds -- failed on the same commit. That is not a
# flake: it is the suite testing a different program from the one under review,
# and it hid a real ingest regression for a full session.
#
# Built here rather than assumed, and the failure is fatal: a suite that cannot
# build the thing it measures has nothing to say about it.
go build -o "$BIN" "$ROOT/cmd/polyemesis" || { echo "cannot build polyemesis"; exit 1; }

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }
note() { printf "        %s\n" "$1"; }

# THE RTMP SINK, and it is a LOOP for the reason this measurement exists.
#
# `ffmpeg -listen 1` accepts exactly one connection and then stops listening --
# acceptance-docker.sh:447 records what that costs when something touches the
# socket early. Here it would be worse than that: the entire question is whether
# an RTMP destination SURVIVES the source seams, and a destination that dies and
# reconnects needs a second accept. A one-shot sink would make every restart look
# like a dead sink, which is the measurement answering itself.
rtmp_sink_loop() {
  while true; do
    ffmpeg -hide_banner -loglevel error -listen 1 \
      -i "rtmp://127.0.0.1:$RTMP_SINK_PORT/live/out" -c copy -f null - >/dev/null 2>&1
    sleep 0.3
  done
}

cleanup() {
  pkill -f "failover-publisher" 2>/dev/null
  [ -n "${RTMP_SINK_PID:-}" ] && kill "$RTMP_SINK_PID" 2>/dev/null
  pkill -f "rtmp://127.0.0.1:${RTMP_SINK_PORT:-0}/live/out" 2>/dev/null
  # INGEST is passed because the ingest ffmpeg's argv carries no work-dir path,
  # so the sweep above cannot see it. It is the port that leaked.
  poly_cleanup_exit "${1:-0}" "$PORT" "$WORK" "$INGEST"
}
trap 'poly_teardown_trap $? cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm
WORK="$(pwd)"
mkdir -p data/recordings
# AN ABSOLUTE DATA DIRECTORY, AND IT IS NOT A TIDINESS PREFERENCE.
#
# THE PLAYLIST TIER CANNOT START WHEN --data IS RELATIVE. Found by this suite,
# on the first run that put a real derivative behind a real concat list.
# engine.reconcilePlaylist builds each entry with playlistmedia.DerivativePath,
# which is relative when DataDir is, and writes those entries into a list that
# lives INSIDE the derivative directory. FFmpeg's concat demuxer resolves a
# relative entry against the LIST FILE's directory, so every path is doubled:
#
#   Impossible to open 'file:data/playlist-media/data/playlist-media/x.ts.v2.ts'
#
# and the tier respawn-loops with the operator told only "No such file or
# directory". Every shipped deployment passes an absolute path
# (deploy/polyemesis.service uses /var/lib/polyemesis, the Dockerfile /data), so
# this is not a production outage -- it is a developer running the binary from a
# working directory, and the engine's unit tests never saw it because they all
# construct absolute paths. Reported rather than fixed here: this suite does not
# get to change engine behaviour to make itself pass.
DATA="$WORK/data"

# Built once rather than `go run` per call. This suite polls status on a half
# second cadence through three waits, and a fresh compile each time would make
# the poll interval meaningless -- the switch would be measured against the Go
# toolchain's speed rather than the tier's.
#
# BUILT FROM $ROOT, IN A SUBSHELL, AND THAT IS NOT COSMETIC. The driver now
# imports scripts/internal/driverlib -- the plumbing it shares with the
# multistream driver -- and `go build` resolves a module import against the
# CURRENT DIRECTORY's go.mod, not against the source file's location. This runs
# after the cd into $WORK, which is outside the module, so a build started here
# would fail with "go.mod file not found" no matter how absolute the source path
# is. A driver importing nothing but the standard library did not care. $DRIVER
# is absolute (WORK was re-anchored to pwd above), so the output still lands
# where the rest of the suite looks for it, and the subshell leaves this
# script's own working directory alone.
DRIVER="$WORK/failover-driver"
( cd "$ROOT" && go build -o "$DRIVER" "$SCRIPTS/acceptance_failover_driver.go" ) || {
  echo "cannot build the driver"; exit 1; }
drive() { "$DRIVER" "http://127.0.0.1:$PORT" "$@" 2>&1; }

# THE PLAYLIST PROFILE, spelled here because the publisher has to match it.
#
# playlistmedia normalises every item to ONE FIXED profile -- 1920x1080 at
# 30 fps, High@4.0, yuv420p -- and nothing derives that from the operator's
# encoder. The playlist feed and the destination both `-c copy` video, so a
# publisher of a different geometry means every live<->playlist cut is a
# mid-stream codec change on the wire. That is a real, unfixed failure, and step
# 10 measures it deliberately; every step before it publishes AT the profile so
# that what they measure is the SWITCH rather than a platform's tolerance for a
# codec change.
#
# Keep these three in step with playlistmedia.NormaliseWidth / NormaliseHeight /
# NormaliseFPS. A drift shows up as the mismatch ratchet in step 10 failing on a
# case that was supposed to match.
PROFILE_W=1920
PROFILE_H=1080
PROFILE_FPS=30
# The mismatch ratchet's publisher: neither the profile's resolution nor its
# frame rate. Both differ on purpose -- an operator who gets one right and the
# other wrong is the common case, and either alone is enough to break `-c copy`.
MISMATCH_W=1280
MISMATCH_H=720
MISMATCH_FPS=60

# publish_geom starts an encoder against the ingest at a given geometry,
# standing in for OBS. Named so cleanup can find it, since it outlives any
# single check.
#
# veryfast with an explicit -profile:v/-level rather than ultrafast, and a
# 2-second GOP: these are playlistmedia's own encoding parameters. ultrafast
# turns CABAC off, which alone drops the stream to a lower H.264 profile than
# every derivative declares -- so an encoder that matched the profile's
# resolution and frame rate would STILL differ from the filler in its bitstream,
# and the "matched" steps would be measuring the mismatch step's subject.
publish_geom() { # width height fps
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=${1}x${2}:rate=${3}" \
    -f lavfi -i "sine=frequency=1000:sample_rate=48000" \
    -metadata comment=failover-publisher \
    -map 0:v -map 1:a -c:v libx264 -preset veryfast \
    -profile:v high -level 4.0 -g "$((2 * $3))" -pix_fmt yuv420p \
    -b:v 3000k -c:a aac -b:a 128k -ac 2 -shortest -t 3600 \
    -f flv "rtmp://127.0.0.1:$INGEST/live/$PUBKEY" \
    > "publisher-$RANDOM.log" 2>&1 &
}
publish() { publish_geom "$PROFILE_W" "$PROFILE_H" "$PROFILE_FPS"; }
unpublish() { pkill -f "failover-publisher" 2>/dev/null; }

# publisher_postmortem says whether the encoder standing in for OBS is even
# alive, and what it last said.
#
# A primary that never goes on air is far more often a publisher that failed to
# connect than a selector that failed to switch -- and the publisher's own log
# is the only place that distinction is written down. Without this, both look
# identical from the status line, which is how issue #38's failure came to be
# reported as "the primary never went on air" when nothing had established that
# the primary was ever publishing.
publisher_postmortem() {
  local f
  if pgrep -f "failover-publisher" >/dev/null 2>&1; then
    note "the publisher process is still running"
  else
    note "the publisher process is GONE -- it exited early or never started"
  fi
  for f in publisher-*.log; do
    [ -f "$f" ] || continue
    [ -s "$f" ] || { note "$f is empty (ffmpeg said nothing)"; continue; }
    note "$f (last 5 lines):"
    tail -5 "$f" | sed 's/^/          /'
  done
  note "server.log (last 8 lines):"
  tail -8 server.log 2>/dev/null | sed 's/^/          /'
}

# waitfor polls the driver's status line until a field matches, or times out.
# The status line is "<active> <switches> <primaryLive> <destRestarts>".
#   waitfor <field-number> <wanted> <seconds>
#
# Delegates to poly_poll_field so a timeout prints the whole trajectory rather
# than returning a bare 1. The caller used to recover context by sampling
# readstatus AGAIN after the deadline, which reports the state at the moment of
# the post-mortem and not the state during the wait -- the two are different
# readings and only the second one answers "was it close?".
waitfor() {
  local idx="$1" want="$2" secs="$3" name
  case "$idx" in
    1) name="the active feed" ;;
    2) name="the switch count" ;;
    3) name="primaryLive" ;;
    4) name="the destination restart count" ;;
    *) name="field $idx" ;;
  esac
  poly_poll_field "$name to become $want" "$idx" "$want" "$secs" readstatus
}

# wait_for_dest_process waits until the status line carries a REAL destination
# restart count, rather than the -1 that means "no destination process was
# reported".
#
# WHY THIS EXISTS. Step 6 -- "THE POINT: the destination never restarted" -- is
# the whole feature, and it compares a count taken before the cut against one
# taken after. The before-read used to be taken a fixed six seconds after the
# primary went on air, and six seconds is not a fact about anything: it is how
# long it happened to take on the machine where the line was written. On the
# OVH box it was not enough, the read came back
#
#     before the cut: primary 2 true -1
#
# and the suite correctly refused to measure. Every behavioural check around it
# passed -- the primary was restored, both switches were recorded, the selector
# moved to the filler -- so the FEATURE was fine and the MEASUREMENT was not.
#
# Note the shape of the bug: a sleep that was doing two jobs. Letting a few
# seconds of primary land in the recording genuinely needs elapsed time. Waiting
# for the destination to start reporting needs a CONDITION, and was being
# covered by the same sleep without anyone saying so. They are separated now.
#
# -1 rather than "unreadable": readstatus validates field 4 with ^-?[0-9]+$,
# which accepts a negative on purpose, so a genuine -1 from the API flows
# through the validator untouched and is indistinguishable here from the
# fallback line. Both mean the same thing to this suite and both are waited out.
wait_for_dest_process() {
  local secs="${1:-30}" deadline=$(( SECONDS + secs )) line
  while [ "$SECONDS" -lt "$deadline" ]; do
    line=$(readstatus)
    if [ "$(printf '%s' "$line" | awk '{print $4}')" != "-1" ]; then
      printf '%s\n' "$line"
      return 0
    fi
    sleep 1
  done
  # Hand back whatever the last read was. The caller reports it as unmeasured,
  # which is the same verdict as before this helper existed -- the point is that
  # it now takes 30 seconds of trying to get there rather than one look.
  printf '%s\n' "$line"
  return 1
}

# settle waits until the active feed has HELD one value for a stretch, rather
# than merely reached it once.
#
#   settle <want> <hold-seconds> <ceiling-seconds>
#
# A NEWLY CONNECTED ENCODER IS NOT STABLE FOR ABOUT FORTY SECONDS, and that is a
# measurement, not a guess. On an idle machine, with nothing but this server and
# one publisher, the selector leaves the primary for the slate and comes back
# roughly 13 seconds and again roughly 36 seconds after the encoder connects,
# with the publisher delivering continuously throughout -- the ingest's own
# bitrate window is what goes briefly quiet, not the wire. Reproduced with the
# 640x360 publisher this suite used before as well, so it is neither new nor
# something the playlist introduced.
#
# It matters here because it is the likely mechanism behind issue #38. Each of
# those spurious switches has to stop a feed, an FFmpeg reading a UDP input that
# has gone quiet cannot notice SIGTERM until its read returns, and the engine
# waits its full 12-second stopTimeout before SIGKILL. Two of those queue behind
# selMu, and a pin POST issued in the middle of one gives up at the driver's
# 30-second client timeout -- which is exactly the "pin auto: context deadline
# exceeded" that issue #38 records without a cause. Anything this suite does
# with a pin therefore waits for the ingest to settle first.
settle() {
  local want="$1" hold="$2" ceil="$3" started held line
  started=$(date +%s); held=0
  while [ $(($(date +%s) - started)) -lt "$ceil" ]; do
    line=$(readstatus)
    set -- $line
    if [ "$1" = "$want" ]; then
      [ "$held" -eq 0 ] && held=$(date +%s)
      [ $(($(date +%s) - held)) -ge "$hold" ] && return 0
    else
      held=0
    fi
    sleep 1
  done
  return 1
}

# readstatus returns a status line with all four fields, retrying a read that
# came back malformed.
#
# The server answers /status while it is swapping a feed, and a truncated body
# there is a transient, not a verdict. Taking it at face value once turned a
# clean run into "the destination restarted (0 -> unexpected)" -- an alarming
# and completely false failure. A check must not report a product fault because
# its own read was short.
readstatus() {
  local i line
  for i in 1 2 3 4 5; do
    line=$(drive status 2>/dev/null | tail -1)
    # Four fields, the last of which is a number: anything else is a partial
    # read or an error message that must not reach the arithmetic below.
    if [ "$(printf '%s' "$line" | wc -w | tr -d ' ')" = "4" ] &&
       printf '%s' "$line" | awk '{exit !($4 ~ /^-?[0-9]+$/)}'; then
      printf '%s\n' "$line"
      return 0
    fi
    sleep 0.5
  done
  printf 'unreadable 0 false -1\n'
}

step "1. Server, with failover and a slate"
"$BIN" -addr ":$PORT" -data "$DATA" -log info > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }

OUT=$(drive)
case "$OUT" in *SETUP_OK*)    ok "first-run setup" ;;          *) bad "setup: $OUT"; exit 1 ;; esac
case "$OUT" in *FAILOVER_OK*) ok "failover enabled with a slate" ;; *) bad "failover: $OUT"; exit 1 ;; esac
case "$OUT" in *DEST_OK*)     ok "one destination on the selector" ;; *) bad "dest: $OUT"; exit 1 ;; esac

# THE PUBLISH KEY. RTMP ingest is now one shared listener for the whole install,
# addressed by stream key, so `rtmp://host:port/live` with no key reaches
# nothing -- the server refuses it at the handshake and the encoder dies with a
# broken pipe several checks later, looking like a failover fault.
#
# This suite published keylessly for its whole life and passed, because the old
# ingest child bound the port itself with `-listen 1` and took whatever arrived.
# It survived the change locally by ACCIDENT: the child still won the race for
# the port often enough on a fast machine. CI lost that race every time.
PUBKEY=$(drive publishkey 2>&1 | tail -1)
case "$PUBKEY" in
  ""|*" "*|*FAIL*|*fail*)
    bad "could not read the source publish key: $PUBKEY"; exit 1 ;;
  *) ok "publish key read from the API ($(printf %s "$PUBKEY" | cut -c1-4)…)" ;;
esac

step "2. Three playlist items, normalised by the production job"
# THE PRODUCTION PATH, END TO END, AND NOTHING IS COPIED BY HAND.
#
# The previous version of this suite did:
#
#     cp data/uploads/filler.ts data/playlist-media/filler.ts.ts
#
# commented as standing in "for the transcode job that writes it in a real
# deployment" -- at a time when no production code registered or submitted that
# job at all. Eighteen checks green, and the playlist could not start on a real
# server. A stand-in for an unwired dependency is indistinguishable from a
# stand-in for a wired one, and that is exactly how it survived. The path in it
# was wrong too by the time this was written: DerivativePath now carries a
# profile version, so the file it wrote was one nothing looked for.
#
# What runs instead is the real chain, in the real order:
#   settings save with playlist items
#     -> api.Server.enqueuePlaylistNormalisation submits one job per upload
#     -> the worker cmd/polyemesis/postprod.go registered writes the derivative
#     -> the finished job reconciles the engine.
# The suite asserts the OUTCOME of that chain through GET /failover/playlist,
# so nothing here can pass on a fixture the suite supplied itself.
#
# STAGED BEFORE THE PUBLISHER, WITH THE TIER OFF, and both halves are load
# bearing. Enqueuing is what a settings save does whether the playlist is
# enabled or not, so the items can be normalised while the tier stays off --
# which matters because the playlist outranks the slate, and a tier already
# running would take the slate's place in the cycle steps 4 and 5 measure.
# Before the publisher, because normalisation is DEFERRED work and the
# governor holds deferred work back while an ingest is live: run this with the
# encoder connected and the jobs sit in StateDeferred until it disconnects.
# That is the governor working, not a fault, and it is why this step is here
# rather than beside the tier it feeds.
FILLER_TONE=2000
# THREE ITEMS OF DIFFERENT LENGTHS, AND A COLOUR EACH.
#
# Three, because one item cannot tell sequencing apart from B1's behaviour of
# playing item 0 for ever: both look like a file on air. Different lengths,
# because with three equal clips a boundary in the wrong place produces exactly
# the same picture as a boundary in the right one. A flat colour each, because
# that is what makes which item is on air readable straight out of the
# destination's own recording -- see step 9.
#
# Deliberately NOT at the normalised profile's geometry: these are the
# OPERATOR'S files, and 1280x720 sources are what the normaliser is for. The
# derivatives it writes are 1920x1080, which is what the publisher matches.
#
# The colours and lengths are read back in step 9, so the three lists below and
# the expectations there are one fact written twice; change them together.
FILLER_ITEMS="filler-a.ts filler-b.ts filler-c.ts"
FILLER_SPECS="filler-a.ts:0xFF0000:3 filler-b.ts:0x00FF00:6 filler-c.ts:0xFF00FF:10"
# A playlist item names a STORED UPLOAD, not a path, so the clips are written
# into the uploads directory rather than anywhere convenient. That is the
# boundary uploads.Store.Resolve defends and the reason items stopped being
# paths.
mkdir -p data/uploads
for spec in $FILLER_SPECS; do
  item="${spec%%:*}"; rest="${spec#*:}"
  colour="${rest%%:*}"; secs="${rest#*:}"
  ffmpeg -hide_banner -loglevel error \
    -f lavfi -i "color=c=$colour:size=1280x720:rate=30" \
    -f lavfi -i "sine=frequency=$FILLER_TONE:sample_rate=48000" \
    -map 0:v -map 1:a -c:v libx264 -preset ultrafast -g 60 -pix_fmt yuv420p \
    -b:v 1200k -c:a aac -b:a 128k -ac 2 -t "$secs" \
    -y "data/uploads/$item" 2>/dev/null
  [ -s "data/uploads/$item" ] || { bad "could not build $item"; exit 1; }
done
OUT=$(drive playlist off $FILLER_ITEMS)
case "$OUT" in *PLAYLIST_OK*) : ;; *) bad "store playlist items: $OUT"; exit 1 ;; esac

# Generous, because this is a real 1080p transcode of three clips at
# NormaliseLimit=1 -- one at a time, on whatever the machine has left. A wait
# that gave up early here would report "the playlist is not ready" for a job
# that was still running, which is the kind of false cause issue #38 exists to
# stop this suite producing.
PLREADY_SECS=240
ready=""
for _ in $(seq 1 $((PLREADY_SECS * 2))); do
  ready=$(drive plready 2>&1 | tail -1)
  case "$ready" in READY) break ;; esac
  sleep 0.5
done
if [ "$ready" = "READY" ]; then
  ok "the settings save queued the transcodes and every item became ready"
else
  bad "no derivative was produced within ${PLREADY_SECS}s: $ready"
  note "nothing in this suite writes a derivative by hand, so an item that is"
  note "not ready means the production enqueue path did not deliver one"
  exit 1
fi

# --- #398 RTMP ARM -------------------------------------------------------------
# Every destination this suite has ever created is kind:file. The 48 runs that
# measured a consumer dying at a seam therefore measured only files, and a file
# that dies rolls over to a new file. An RTMP destination that dies drops and
# reconnects to the platform. That is a different severity and it has never been
# measured. This arm asks only that one question and asserts nothing, so it
# cannot change what any existing step measures.
RTMP_SINK_PORT=1936
rtmp_sink_loop &
RTMP_SINK_PID=$!
sleep 1
OUT=$(drive addrtmp rtmpdest "rtmp://127.0.0.1:$RTMP_SINK_PORT/live" out 2>&1)
case "$OUT" in
  *DEST_OK*) note ">>>RTMP the rtmp destination was created" ;;
  *) note ">>>RTMP could not create the rtmp destination: $OUT" ;;
esac
sleep 6
note ">>>RTMP baseline restarts=$(drive restarts rtmpdest | tail -1) outtime=$(drive outtime rtmpdest | tail -1)ms"

step "3. The primary goes on air"
# Before publishing, not after. The server reporting ready means its HTTP
# listener is up; the ingest child is spawned after that and binds 1938 a moment
# later. A publisher that arrives first is refused and exits, and the wait below
# then spends its whole 40s ceiling on a primary that was never coming.
poly_wait_port_ready "$INGEST" 15 || true
publish
if waitfor 1 primary 40 ; then
  ok "the primary is on air once it delivers"
else
  bad "the primary never went on air within 40s"
  publisher_postmortem
  exit 1
fi
# Let a few seconds of real primary land in the file before anything is cut.
# This one IS about elapsed time -- there has to be primary in the recording for
# the timeline check at step 9 to have anything to find.
sleep 6

# And this one is about a condition, which the sleep above was quietly being
# asked to cover as well. See wait_for_dest_process.
if ! BEFORE=$(wait_for_dest_process 30); then
  note "the destination process was still unreported after 30s; step 6 will say so"
fi
set -- $BEFORE; restarts_before="${4:-0}"
note "before the cut: $BEFORE"

step "4. The encoder disappears — the slate takes over"
unpublish
# Grace is 2s; allow generously for the sweep and the feed swap.
if waitfor 1 slate 30 ; then
  ok "the slate went on air after the grace period"
else
  bad "the slate never took over within 30s"
  publisher_postmortem
fi
sleep 6

step "5. The encoder returns — auto-return puts the primary back"
publish
if waitfor 1 primary 40 ; then
  ok "the primary was restored automatically"
else
  bad "auto-return never happened: $(readstatus)"
fi
# Not a `sleep 6`. Everything from step 7 on drives the selector by hand, and a
# pin issued while the ingest is still in its unsettled first half-minute is
# issue #38 -- see settle for the measurement behind that. This waits for a
# quiet stretch instead of guessing at one, and reports what it saw if the
# stretch never comes rather than sailing on into a failure with no cause.
if settle primary 25 120; then
  note "the ingest settled; the primary has held for 25s"
else
  note "the ingest never held the primary for 25s in 120s: $(readstatus)"
  note "expect issue #38 below: a pin issued mid-switch waits on a 12s process stop"
fi

AFTER=$(readstatus)
set -- $AFTER; active_after="$1"; switches="${2:-0}"; restarts_after="${4:-0}"
note "after the cycle: $AFTER"

step "6. THE POINT: the destination never restarted"
# This is the whole feature. If the destination restarted, the platform
# connection dropped and failover achieved nothing -- the output file could
# still look perfectly healthy.
if [ "$restarts_before" = "-1" ] || [ "$restarts_after" = "-1" ]; then
  bad "no destination process was reported; nothing was measured"
elif [ "$restarts_after" -eq "$restarts_before" ]; then
  ok "the destination rode both switches without restarting ($restarts_after restarts)"
else
  bad "the destination restarted across the switch ($restarts_before -> $restarts_after)"
  note "a restart drops the platform connection, which is what failover exists to prevent"
fi

if [ "${switches:-0}" -ge 2 ]; then
  ok "the tier recorded both switches ($switches)"
else
  bad "expected at least 2 switches, saw ${switches:-0}"
fi

# THE CEILING. Issue #226: the floor above is half an assertion, and it is the
# wrong half. Applying the wrong-hub mutation to sampleSources -- `primaryRx :=
# e.downstreamHub().RxBytes()`, the exact error that function's own comment warns
# against -- made this tier switch 80 times against a baseline of 6, and the
# check above printed "the tier recorded both switches (80)". It was caught, but
# only by four downstream consequence checks; the assertion whose whole job is to
# describe the switch count reported success.
#
# A CONSTANT `-le` IS NOT THE FIX. The legitimate count here is timing-dependent
# -- the switches land at roughly 13s and 36s depending on how the sweep and the
# 5s grace line up -- so any number is either loose enough to admit flapping or
# tight enough to redden correct code. What is not timing-dependent is that a
# tier nobody is asking to switch STOPS switching. That is the property.
#
# The hold is deliberately weaker than the `settle primary 25 120` that precedes
# it: the active feed holding one value for 25s already implies the switch count
# held for 25s, so on a run where that settle succeeded this cannot fail. It
# earns its keep on the runs where it did not -- which is precisely the mutant's
# signature, and where the suite currently only prints a note.
if poly_hold_field "the switch count" 2 10 40 readstatus; then
  ok "the switch count has stopped moving (held at $POLY_HELD_VALUE for 10s)"
else
  bad "the tier is still switching with nothing asking it to: the switch count changed $POLY_HELD_CHANGES time(s) in 40s and never held one value for 10s"
  note "that is FLAPPING, which a floor of 2 cannot see -- see issue #226"
fi

step "7. Filler starts playing, with the encoder still on air"
# THE CASE THIS WHOLE SUB-PROJECT EXISTS FOR, and it was impossible to write
# before it. A playing file used to feed the PRIMARY's hub, so the primary
# always had bytes on it, the selector read the programme as live, and it would
# never switch to a backup or a slate. Playing a file silently disabled the
# entire failover feature -- everything steps 2 to 5 just measured turned itself
# off the first time anybody put a file on air, and nothing said so.
#
# The playlist is enabled HERE rather than in step 2 deliberately: it outranks
# the slate, so a tier running from the start would have taken the slate's place
# in step 4 and this suite would have stopped measuring the cycle it was
# originally written for. Only the ENABLE happens here -- the items and their
# derivatives were produced in step 2 by the production job, and nothing in this
# suite writes a derivative by hand.
OUT=$(drive playlist on $FILLER_ITEMS)
case "$OUT" in *PLAYLIST_OK*) : ;; *) bad "enable playlist: $OUT"; exit 1 ;; esac

# The pin is how this suite SEES the file delivering while the encoder is still
# connected, and it has to see that before cutting the encoder or the step below
# proves nothing about filler. A pin is honoured only while its source is
# actually delivering, so the playlist becoming active IS the evidence that its
# own hub is carrying bytes. There is no status field to read instead, and "the
# tier is running" would be the wrong question anyway: a path that is confined
# but names no file leaves FFmpeg backing off forever with the process reported
# healthy the whole time.
OUT=$(drive pin playlist)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin playlist: $OUT"; exit 1 ;; esac
if waitfor 1 playlist 40 ; then
  ok "the filler is on air while the encoder is still publishing"
else
  # Not fatal. The checks below say what happened when the encoder dropped, and
  # a run that stopped here would report a cause nobody had measured -- the
  # habit issue #38 exists to break.
  bad "the filler never went on air, so nothing was playing when the encoder dropped"
  publisher_postmortem
fi

OUT=$(drive pin auto)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin auto: $OUT"; exit 1 ;; esac
if waitfor 1 primary 40 ; then
  ok "a live encoder pre-empts the filler as soon as the pin is released"
else
  bad "the primary never came back after the pin was released: $(readstatus)"
fi
sleep 4

step "8. THE POINT: the encoder drops while the filler plays"
unpublish
# THE REGRESSION, in one field. Before the playlist got a hub of its own, the
# file's bytes landed on the PRIMARY's relay, so primaryLive stayed true for as
# long as the file played -- with no encoder connected at all -- and every
# failover decision underneath it became unreachable.
if waitfor 3 false 30 ; then
  ok "the primary is seen to go down even though a file is on air"
else
  bad "primaryLive stayed true with no encoder connected -- the filler is feeding the primary's hub"
  publisher_postmortem
fi
if waitfor 1 playlist 30 ; then
  ok "the selector left the dead primary and put the filler on air"
else
  bad "the selector never switched away from the primary while the filler played"
fi

FILLER_STATUS=$(readstatus)
set -- $FILLER_STATUS; restarts_filler="${4:-0}"
note "with the filler on air: $FILLER_STATUS"
# Same measurement as step 6, against the same baseline: a switch that restarts
# a destination drops the platform connection, and it does that whether the
# source arriving is a slate or a scheduled programme.
# The BASELINE is checked as well as the reading, and that asymmetry was a real
# false failure. Observed on run 6 of 14 while measuring #126: restarts_before
# came back -1 -- step 6 caught that and said "nothing was measured" -- and then
# this comparison read -1 against a perfectly healthy 0 and reported "the
# destination restarted when the filler went on air (-1 -> 0)". Nothing had
# restarted. A missing measurement was being rendered as the exact failure this
# suite exists to detect, in a suite whose whole value is that it is trusted
# when it says that.
#
# Reported as unmeasured rather than passed: the destination may genuinely have
# restarted and we would not know. The one thing it must not do is claim a
# restart it did not observe.
if [[ "$restarts_filler" = "-1" ]] || [[ "$restarts_before" = "-1" ]]; then
  bad "no destination process was reported across the filler switch; nothing was measured"
elif [ "$restarts_filler" -eq "$restarts_before" ]; then
  ok "the destination rode the switch to filler without restarting ($restarts_filler restarts)"
else
  bad "the destination restarted when the filler went on air ($restarts_before -> $restarts_filler)"
fi

# THE SEQUENCING WINDOW. Nothing is asserted here; this is the recording step 9
# reads.
#
# Long enough to contain a WHOLE run of every item wherever in the cycle the
# switch happened to land. The tier has been playing into its own hub since
# step 7, so the selector joined it mid-item and the window starts mid-run: one
# full cycle guarantees every item appears, and a second guarantees at least one
# appearance of each is bounded on both sides and therefore measurable. The
# items total 19s, so two cycles is 38s and the wait is 42.
SEQUENCE_SECS=42
note "letting the playlist run ${SEQUENCE_SECS}s so a whole cycle lands in the recording"
sleep "$SEQUENCE_SECS"

step "9. The output timeline, and what actually played"
# ONE FILE, EVERY SWITCH IN THE RUN. This is the whole of steps 3 to 8 measured
# end to end, not the slate cycle alone: the recording spans primary -> slate ->
# primary (steps 3 to 5), primary -> filler and back when the pin goes on and
# off (step 7), and primary -> filler when the encoder is cut (step 8). Five
# switches, five chances to hand a platform a timestamp that goes backwards, and
# the checks below cover all of them at once.
#
# The output is checked, not discarded. `drive stopall` used to be run with its
# output thrown away, and it had been failing silently for as long as it existed
# -- it read `id` off a list whose rows are {"destination": ..., "routing": ...},
# got 0 every time, and POSTed /destinations/0/stop for a 404 nobody looked at.
# No destination was ever stopped, so this file was always an unfinalised
# Matroska, and the duration check below was written around that damage instead
# of against it.
OUT=$(drive stopall)
case "$OUT" in *STOPPED*) : ;; *) bad "stop the destinations: $OUT" ;; esac
sleep 8
# MPEG-TS, NOT MATROSKA, AND THE DETECTOR BELOW IS WHY. Issue #126.
#
# Matroska stores no DTS. ffprobe's dts_time for an .mkv is RECONSTRUCTED from
# the PTS reorder buffer, so the "backwards decode timestamp" this suite has
# been prosecuting since #126 was filed is a property of that reconstruction as
# much as of the timeline. Measured on one encoder writing 90 identical packets
# to both containers:
#
#   onair.mkv   90 packets, 2 with dts_time=N/A
#   onair.ts    90 packets, 0 with dts_time=N/A
#
# A container that STORED dts could not answer N/A. It is reconstructing, and a
# reconstruction has been shown to fail in both directions from the same
# packets: a real clamped backward step at the destination read CLEAN from the
# .mkv, and a wrong-extradata case read ELEVEN backward steps, worst 66ms, from
# the .mkv against ZERO from the .ts.
#
# mpegts writes DTS into the stream as the muxer received it, so the number this
# suite asserts on becomes a measurement rather than an inference. Nothing else
# here depends on the container: the silence, duration and sequence checks below
# all demux either one.
OUTFILE=data/recordings/onair.ts
if [ ! -s "$OUTFILE" ]; then
  bad "no output was produced, so the timeline could not be measured"
else
  ok "the destination produced a continuous output across every switch in the run"

  # TIMELINE MONOTONICITY. A feed started without -output_ts_offset republishes
  # from zero, so a switch hands the destination a timestamp behind the one
  # before it, and a platform answers a backwards jump by dropping the
  # connection. Counting backwards steps is the direct measurement of the flag
  # working, and one count over this file covers every switch above.
  #
  # DTS, NOT PTS. The first version of this check measured pts_time and reported
  # 268 backwards steps on a stream that was completely healthy. Presentation
  # timestamps are not required to be monotonic in packet order -- frame
  # reordering means a correct encoder emits them out of order routinely.
  # DECODE timestamps are the ones that must never go backwards, and they are
  # what a receiving platform's demuxer trips over. Measured against the same
  # file: 268 backwards PTS steps, 0 backwards DTS steps.
  # REPORT THE STEP, NOT ONLY THE COUNT.
  #
  # This check used to print a bare number, and issue #126 is the cost of that:
  # the fault has appeared under two unrelated timing changes over several weeks
  # and every occurrence recorded the same single digit, so no run could be
  # compared with any other. Where the step lands and how big it is are the two
  # facts that separate the candidate mechanisms -- a straggler from the
  # outgoing feed arriving after the incoming one has started is a small step at
  # a switch, and a feed republishing from zero is a large one at the same
  # place. One line per step turns the next occurrence into evidence.
  # THE PACKET LIST IS CAPTURED ONCE, to a file. The threshold assertion below
  # and the per-seam table under it both read it, and probing the file twice
  # would let the two halves of one report disagree about what they measured.
  # It is also the artifact a failed CI run needs: the mkv is uploaded now, but
  # this is the derived form anybody actually reads.
  ffprobe -v error -select_streams v -show_entries packet=dts_time \
         -of csv=p=0 "$OUTFILE" 2>/dev/null > dts.csv
  dtsreport=$(awk -F, 'BEGIN{prev=-1e9;n=0;i=0}
           {if ($1=="N/A") next; i++; t=$1+0;
            if (t < prev - 0.001) {
              n++
              printf "step %d at packet %d: %.3f -> %.3f, back %.6fs\n", n, i, prev, t, prev-t
            } else if (t < prev && prev-t > worst) {
              # BELOW THE THRESHOLD IS STILL EVIDENCE. See #126: the leading
              # explanation is that a small backward step exists all the time
              # and a change that widens the offset at the seam merely pushes it
              # over 1ms. If that is right, this line is non-empty on runs that
              # PASS, and the question is settled without waiting for a failure.
              worst=prev-t; worsti=i; worstp=prev; worstt=t
            }
            prev=t}
           END{printf "COUNT %d\n", n+0
               if (worst > 0) printf "NEARMISS at packet %d: %.6f -> %.6f, back %.6fs\n", worsti, worstp, worstt, worst}' dts.csv)
  back=$(printf '%s\n' "$dtsreport" | awk '/^COUNT /{print $2}')
  if [ "${back:-1}" -eq 0 ]; then
    ok "no backwards decode timestamp across any switch in the run"
    # THE NEAR MISS PRINTS HERE TOO, and printing it only on failure defeated
    # the whole point of measuring it. #126's leading explanation is that a
    # small backward step exists at the seam all the time and a widened offset
    # merely pushes it over 1ms -- which can only be confirmed or refuted from
    # runs that PASS. Emitting it exclusively in the failure branch meant
    # waiting for the very event the line was added to avoid waiting for.
    printf '%s\n' "$dtsreport" | grep '^NEARMISS ' | while IFS= read -r line; do
      note "$line"
    done
  else
    bad "$back backwards DTS step(s) — a platform drops the connection on these"
    printf '%s\n' "$dtsreport" | grep -v '^COUNT ' | while IFS= read -r line; do
      note "$line"
    done
  fi

  # THE SEAM TABLE, AND IT PRINTS ON PASS AS WELL AS FAIL.
  #
  # The count above is one number for the whole file, and #126 has now been
  # looked at through that number for several weeks without it settling
  # anything. It cannot distinguish "one sub-millisecond step somewhere" from "a
  # sub-millisecond step at EVERY switch", and those two are the leading
  # hypothesis and its refutation: if a small backward step is present at every
  # seam all the time, then a timing change that widens it merely pushes an
  # existing step over the threshold, and the offset was never the cause.
  #
  # So each switch gets a row, from the ledger the engine now writes
  # (internal/engine/selector.go, logSeam). Every row is available on a run that
  # PASSES, which is the whole point -- there is no need to wait for another
  # failure to collect it.
  #
  # THE TWO AXES ARE NOT CALIBRATED, AND THIS TABLE DOES NOT PRETEND THEY ARE.
  #
  # The first version of this block assigned each backward step to the last seam
  # at or before it, treating a packet's DTS in the file and a feed's offset on
  # the tier as one axis. Six runs later one of them FAILED -- a 30ms step at file
  # DTS 25.898 -- and the assignment was wrong. The audio says where the step
  # really was: the slate's silence in that file ended at 25.703, so the step sits
  # 195ms after the slate handed back to the primary, which is seam 3 at tier
  # 29.837. The identity mapping had blamed seam 2, at tier 14.339, a whole
  # switch earlier.
  #
  # The two axes are not a constant apart either. In that run the slate held the
  # air for 15.498s of tier time and occupies 16.706s of the file, so the offset
  # implied by the start of the silence (5.34s) and by its end (4.13s) disagree
  # by more than a second. A destination rebases the hub's timeline to its own
  # first packet, and the feeds do not publish at exactly tier rate -- the same
  # ahead-of-realtime effect this whole investigation is about.
  #
  # So the table reports FACTS FROM EACH SIDE and no join: the ledger's rows, the
  # steps with their positions in the file, and the slate window as an anchor a
  # reader can line the two up with by hand. A wrong attribution is worse than
  # none here, because it would send the next person to the wrong switch with a
  # number that looks authoritative.
  grep 'msg="feed seam"' server.log > seams.txt 2>/dev/null
  epoch=$(awk '/msg="source selector started"/{
            for (i=1;i<=NF;i++) if ($i ~ /^tierEpoch=/) { print substr($i,11); exit }}' server.log)
  [ -n "${epoch:-}" ] && note "tier epoch $epoch (every seam offset below is seconds from here)"
  # THE ANCHOR. Only the slate is silent -- the publisher sends a 1 kHz tone and
  # the filler a 2 kHz one -- so the long silence in the file is the slate's time
  # on air, and it is the one landmark visible on BOTH axes. The check below
  # reads the same pass, so the two cannot disagree.
  ffmpeg -hide_banner -nostats -i "$OUTFILE" \
         -af "silencedetect=n=-50dB:d=1" -f null - > silence.txt 2>&1
  awk '{for (i=1;i<=NF;i++) {
          if ($i=="silence_start:") st=$(i+1)+0
          else if ($i=="silence_end:")
            printf "anchor: the file is silent from %.3f to %.3f (%.3fs) -- only the slate is\n",
                   st, $(i+1)+0, $(i+1)-st }}' silence.txt | while IFS= read -r line; do
    note "$line"
  done
  awk '
      # First file: one line per handover, as slog key=value.
      #
      # Selected by FILENAME and NOT by the usual FNR==NR idiom, which is wrong
      # in exactly the case that matters here: when the ledger is EMPTY, NR and
      # FNR agree on the second file too, and every packet timestamp is read as
      # a handover. A run with no seam lines then reported thirteen switches from
      # a thirteen-packet fixture. Caught by the seeded fixture this awk was
      # checked against before it went anywhere near CI.
      FILENAME=="seams.txt" {
        split("", kv)
        for (i=1;i<=NF;i++) { p=index($i,"="); if (p>1) kv[substr($i,1,p-1)]=substr($i,p+1) }
        n++
        at[n]=kv["inOffset"]+0; wall[n]=kv["time"]
        from[n]=kv["outKind"]; to[n]=kv["inKind"]
        tear[n]=kv["teardownMs"]+0
        out_t[n]=kv["outTimeMs"]+0; out_o[n]=kv["outOffset"]+0
        dead[n]=kv["stopDeadline"]
        next
      }
      # Second file: the packet DTS list, one value per line.
      $1=="N/A" { next }
      { t=$1+0
        # EVERY backward step, at any magnitude. The threshold belongs to the
        # assertion; a table that applied it too would answer the question it
        # was written to answer with the number it was written to replace.
        if (have && t<prev) { s++; spos[s]=prev; sto_[s]=t; ssize[s]=prev-t }
        prev=t; have=1 }
      END {
        if (n==0) { print "SEAMS none in server.log -- either no switch happened or the build predates the ledger"; exit }
        printf "SEAMS %d handover(s); %d backward DTS step(s) of any magnitude in the file\n", n, s+0
        for (j=1;j<=n;j++) {
          # The outgoing terms are printed RAW. A derived figure used to sit
          # here called "predicted", and it was minus the start lag of the
          # outgoing feed rather than a timestamp step -- see logSeam and #126.
          # Two proposed fixes were reasoned from it and both were refuted, so
          # this table now reports what was measured and leaves the arithmetic
          # to a reader who has established what the terms mean.
          printf "  seam %d %s->%s at tier %.3fs (%s): teardown %.3fms, outOffset %.3fs, outTime %.0fms",
                 j, from[j], to[j], at[j], wall[j], tear[j], out_o[j], out_t[j]
          if (dead[j]=="true") printf ", STOP DEADLINE -- the old feed may still have been writing"
          printf "\n"
        }
        # The steps are listed on the FILE axis, next to the seams on the TIER
        # axis, and deliberately not joined. See the note above the awk: the two
        # are not a constant apart, and a wrong attribution reads as an answer.
        for (j=1;j<=s;j++)
          printf "  step %d at file dts %.6f -> %.6f, back %.6fs\n", j, spos[j], sto_[j], ssize[j]
      }' seams.txt dts.csv | while IFS= read -r line; do
    note "$line"
  done

  # The switch must be visible IN THE BYTES, not only in the status field. The
  # publisher sends a 1 kHz tone and the slate is silent, so a file that carries
  # both means the destination really was fed from two different sources. Only
  # the SLATE period can produce silence -- the filler in steps 6 and 7 carries a
  # 2 kHz tone of its own -- so this stays a check on the slate specifically even
  # though the file now spans the filler switches too.
  # Read from the pass captured above rather than probing the file again: the
  # seam table prints the same windows as its anchor, and two passes could
  # disagree about where the slate was.
  quiet=$(grep -c "silence_start" silence.txt || true)
  if [ "${quiet:-0}" -ge 1 ]; then
    ok "the slate period is present in the audio (tone -> silence -> tone)"
  else
    bad "the output never went quiet; the slate was not actually on air"
  fi

  # Span from the last decode timestamp rather than format=duration. A
  # destination stopped mid-write leaves an unfinalised Matroska whose header
  # carries no duration at all -- ffprobe answers "N/A", which is not zero and
  # not a failure, and comparing it as a number is how the first version of this
  # check produced "the output is only N/As".
  dur=$(ffprobe -v error -select_streams v -show_entries packet=dts_time \
        -of csv=p=0 "$OUTFILE" 2>/dev/null |
        awk -F, '{if ($1!="N/A") last=$1+0} END{printf "%d", last}')
  if [ "${dur:-0}" -ge 15 ]; then
    ok "the output spans the whole run, slate cycle and filler switches (${dur}s)"
  else
    bad "the output spans only ${dur:-0}s; it did not survive the run"
  fi

  # ------------------------------------------------------ WHAT ACTUALLY PLAYED
  #
  # THE ONE THING THIS SUB-PROJECT IS FOR. B1 played item 0 and only item 0,
  # for ever, and every check above would pass on that behaviour unchanged: a
  # file was on air, the timeline was monotonic, the destination never
  # restarted. Sequencing is only visible in the PICTURE.
  #
  # Each item is a flat colour, so one pixel per half second says which item was
  # on air. The frame is averaged down to 1x1 (scale=...:flags=area, which
  # really averages -- the default filter would sample) and read as raw RGB, so
  # this needs no filter that reports through a log line and no per-frame
  # parsing. rgb24 bytes are UNSIGNED, hence "%u": "%d" prints red as -2 0 0.
  #
  # The awk then finds the LONGEST unbroken stretch of filler colours -- the
  # step 8 window, not the few seconds the pin bought in step 7 -- and reports
  # what happened inside it. Run lengths are counted only for runs bounded on
  # BOTH sides, because the first and last are cut off by the window's edges and
  # would read short.
  SAMPLE_HZ=2
  seqline=$(ffmpeg -hide_banner -loglevel error -i "$OUTFILE" \
            -vf "fps=$SAMPLE_HZ,scale=1:1:flags=area" -f rawvideo -pix_fmt rgb24 - 2>/dev/null |
            hexdump -v -e '3/1 "%u "' -e '"\n"' |
            awk '
    { r=$1+0; g=$2+0; b=$3+0; lab="-"
      # The three filler colours, generous on tolerance: these survive a
      # yuv420p round trip almost exactly, so anything near-miss is not filler.
      if (r>205 && g<50 && b<50)  lab="1"
      else if (r<50 && g>205 && b<50)  lab="2"
      else if (r>205 && g<50 && b>205) lab="3"
      seq[n++]=lab }
    END {
      start=-1; len=0; i=0
      while (i<n) {
        if (seq[i]=="-") { i++; continue }
        j=i; while (j<n && seq[j]!="-") j++
        if (j-i>len) { len=j-i; start=i }
        i=j
      }
      if (len==0) { print "distinct=0 outoforder=0 run1=0 run2=0 run3=0 cycle=0 window=0"; exit }
      bad=0; prev=""; k=start; end=start+len; lastb=-1; cycle=0
      while (k<end) {
        lab=seq[k]; m=k
        while (m<end && seq[m]==lab) m++
        seen[lab]=1
        if (k>start && m<end && m-k > run[lab]+0) run[lab]=m-k
        # The cycle is measured from one item-2 run to the next: the distance
        # between two appearances of the SAME item is a whole lap of the
        # playlist however the window happened to be cut.
        if (lab=="2" && k>start) { if (lastb>=0 && m<end) cycle=k-lastb; lastb=k }
        if (prev!="" && !((prev=="1"&&lab=="2")||(prev=="2"&&lab=="3")||(prev=="3"&&lab=="1"))) bad++
        prev=lab; k=m
      }
      d=0; for (x in seen) d++
      printf "distinct=%d outoforder=%d run1=%d run2=%d run3=%d cycle=%d window=%d\n",
             d, bad, run["1"]+0, run["2"]+0, run["3"]+0, cycle, len
    }')
  note "what played: $seqline"
  set -- $seqline
  seq_distinct="${1#*=}"; seq_outoforder="${2#*=}"
  seq_run1="${3#*=}"; seq_run2="${4#*=}"; seq_run3="${5#*=}"; seq_cycle="${6#*=}"

  # 1. MORE THAN THE FIRST ITEM. Three distinct colours in one unbroken filler
  #    window is the direct refutation of play-item-0-for-ever, and it is the
  #    check that could not exist before this sub-project.
  if [ "${seq_distinct:-0}" -eq 3 ]; then
    ok "all three items reached the destination, not the first one over and over"
  else
    bad "only ${seq_distinct:-0} of 3 items ever reached the destination"
    note "one colour means the tier played item 0 and stopped, which is the behaviour B2 replaced"
  fi

  # 2. IN THE ORDER THE PLAYLIST NAMES THEM. A concat list assembled in the
  #    wrong order, or a wrap that restarts somewhere other than the top, shows
  #    up as a transition that is not a -> b -> c -> a.
  if [ "${seq_outoforder:-1}" -eq 0 ]; then
    ok "the items played in the order the playlist names them, and wrapped to the top"
  else
    bad "${seq_outoforder} transition(s) did not follow the playlist's order"
  fi

  # 3. FOR ITS OWN LENGTH, AND A WHOLE LAP FOR THE SUM OF THEM. THIS is why the
  #    three clips are 3s, 6s and 10s rather than three of the same length: with
  #    equal items a boundary in the wrong place produces a picture identical to
  #    a boundary in the right one, so the lengths are the only evidence that the
  #    seams land where the media says they do.
  #
  #    MEASURED ON ITEM 2 AND ON THE LAP, NOT ON EVERY ITEM, and the reason is
  #    itself a measurement. Items 1 and 3 sit either side of the LOOP boundary,
  #    and FFmpeg's concat demuxer under `-c copy` does not make that boundary
  #    cleanly: the last item's final frame holds for about 2.5 seconds and the
  #    first item then plays about 2 seconds short. Reproduced with nothing but
  #    ffmpeg, the same `-stream_loop -1 -re -fflags +genpts -f concat -c copy`
  #    argv playlistFeedArgs builds, and the derivatives straight off disk:
  #
  #      1:6 2:12 3:25 1:2 2:12 3:24 1:3 2:6      (samples at 2 Hz)
  #
  #    where 6/12/20 is what the media says. The derivatives themselves probe at
  #    3.12s, 6.12s and 10.12s, so it is not a normalisation fault, and the
  #    output timeline stays monotonic through it -- the DTS check above passes.
  #    It is written down here rather than asserted because it is a property of
  #    the demuxer, not of anything B2 chose; a check pinned to it would fail on
  #    a different FFmpeg for a reason nobody could act on. What IS asserted is
  #    what that artefact does not touch:
  #
  #      - item 2 is the one item never adjacent to the loop boundary, so its
  #        length is exact evidence that an interior seam lands on the media.
  #      - a whole lap must equal the sum of the three items. That is what says
  #        no item is skipped, repeated, or silently stretched to fill: the wrap
  #        moves 2 seconds from one item to another and does not change the lap.
  runs_ok=1
  if [ "${seq_run2:-0}" -lt 11 ] || [ "${seq_run2:-0}" -gt 13 ]; then
    runs_ok=0
    note "item 2 played for ${seq_run2:-0} samples, expected 12 (+/-1) at ${SAMPLE_HZ}Hz"
  fi
  # 3 + 6 + 10 seconds at SAMPLE_HZ, with two samples of slack: one for the
  # sampling phase and one for the padding the normaliser adds per item.
  if [ "${seq_cycle:-0}" -lt 36 ] || [ "${seq_cycle:-0}" -gt 40 ]; then
    runs_ok=0
    note "a whole lap took ${seq_cycle:-0} samples, expected 38 (+/-2) at ${SAMPLE_HZ}Hz"
  fi
  if [ "$runs_ok" -eq 1 ]; then
    ok "the seams follow the media: item 2 ran ${seq_run2}, a whole lap ${seq_cycle} samples at ${SAMPLE_HZ}Hz"
    note "items 1 and 3 measured ${seq_run1} and ${seq_run3} against 6 and 20; the loop boundary moves about 2s between them"
  else
    bad "the seams are not where the media says"
  fi
fi

step "10. THE FAILURE THIS DOES NOT FIX: a publisher that does not match the profile"
# MEASURED, NOT FIXED, AND PINNED SO IT CANNOT SILENTLY GET WORSE.
#
# Items are normalised to match EACH OTHER -- one fixed 1920x1080@30 profile, so
# the concat demuxer will splice them. Nothing normalises them to match the
# OPERATOR'S ENCODER, and nothing can without either constraining the ingest or
# re-encoding at the selector, and the latter reverses a decision made
# throughout the engine. So an operator publishing at any other geometry hands
# every destination a mid-stream codec change at every live<->playlist cut,
# because the playlist feed and the destination both `-c copy` video.
#
# EVERY STEP ABOVE HIDES THIS ON PURPOSE, exactly as the old suite did: they
# publish AT the profile, so what they measure is the selector's switch rather
# than a platform's tolerance for a codec change. This step does the opposite
# and reports the number.
#
# THE NUMBER BELOW WAS MEASURED, NOT CHOSEN. It is what this machine observed
# across one cut to the playlist and one cut back, with a 1280x720@60 publisher
# against 1080p30 filler, and it came out ZERO -- which is a smaller number than
# it looks, and must not be read as "the mismatch is harmless".
#
# THIS SUITE'S DESTINATION IS A FILE, AND A FILE CANNOT DROP. The Matroska muxer
# takes the codec parameters of the first packets it sees, writes them into the
# header once, and then accepts everything after it without complaint. So the
# destination process survives the cut and the restart count stays at zero,
# while the recording it produced declares ONE geometry for content that
# contains two -- the note printed below shows it. An RTMP platform has no
# equivalent of that: it reads the header, receives a different stream, and
# closes the connection. What this case can measure on a file destination is
# therefore the weaker half of the failure; the stronger half is written down
# in docs/SCHEDULED-BROADCAST.md where an operator meets it.
#
# Pinned anyway, because the guarantee is one-directional and still worth
# having: a change that makes destinations restart at that cut when they did not
# before fails here. Do not raise it to make a run pass. A rise is the finding.
EXPECTED_MISMATCH_RESTARTS=0
# Its own destination, and it has to be its own. This case EXPECTS restarts, and
# a restarting file destination truncates and reopens its output -- pointed at
# onair.mkv it would erase the recording step 9 just measured. It is added after
# those measurements for the same reason.
OUT=$(drive adddest mismatch mismatch.mkv)
case "$OUT" in *DEST_OK*) : ;; *) bad "add the mismatch destination: $OUT"; exit 1 ;; esac
publish_geom "$MISMATCH_W" "$MISMATCH_H" "$MISMATCH_FPS"
if waitfor 1 primary 40 ; then
  ok "the mismatched encoder is on air"
else
  bad "the mismatched encoder never went on air within 40s"
  publisher_postmortem
fi
# The baseline is taken AFTER the primary has settled, so the destination's own
# start -- which lands on filler and is then cut to the encoder -- is outside
# the measurement. What is measured is the two deliberate cuts below, and only
# those: a spurious switch during the unsettled window would be counted as a
# restart this case caused, which is precisely the number that must not drift.
settle primary 25 120 || note "the mismatched ingest never settled; the count below may include a switch this case did not make"
# PRODUCED MEDIA IS SAMPLED ACROSS THE RUN. Issue #275.
#
# The assertion at the end of this step reads the recording's SIZE once, after
# everything, and a zero there has causes it cannot separate:
#
#   never delivered   the destination sat on a hub that was closed under it,
#                     started and idle -- the 76-second case this step exists
#                     for, and a product defect
#   truncated late    a file destination truncates and reopens when it restarts,
#                     so a restart near the end zeroes a file that was fine all
#                     run -- this step tearing down over its own measurement
#
# THE FILE IS NOT AN OBSERVABLE OF DELIVERY. Sampling its size does not separate
# them, and that was measured rather than assumed: on a fully healthy local run
# the recording read 0 bytes at every point during the run and 262144 at the
# end, because the MKV muxer buffers and flushes once at close. A healthy run
# and the closed-hub failure share the identical zero prefix.
#
# out_time does separate them. It counts the media the child has PRODUCED, moves
# with delivery rather than with the muxer's flush schedule, and the server has
# been publishing it on /status all along -- engine.DestStatus.Process is a whole
# supervisor.Status. Only the driver's decode struct was missing it.
mis_t_settled=$(drive outtime mismatch | tail -1)
mis_before=$(drive restarts mismatch | tail -1)
OUT=$(drive pin playlist)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin playlist (mismatch): $OUT" ;; esac
waitfor 1 playlist 40 || note "the mismatched run never reached the playlist; the count below covers less than two cuts"
sleep 8
OUT=$(drive pin auto)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin auto (mismatch): $OUT" ;; esac
waitfor 1 primary 40 || note "the mismatched run never came back to the encoder"
sleep 8
mis_t_back=$(drive outtime mismatch | tail -1)
mis_after=$(drive restarts mismatch | tail -1)
note "restarts across the mismatched cuts: $mis_before -> $mis_after"
if [ "${mis_before:--1}" = "-1" ] || [ "${mis_after:--1}" = "-1" ]; then
  bad "no mismatch destination process was reported; nothing was measured"
else
  mis_delta=$((mis_after - mis_before))
  if [ "$mis_delta" -le "$EXPECTED_MISMATCH_RESTARTS" ]; then
    ok "the mismatched cut cost $mis_delta restart(s), at or under the pinned $EXPECTED_MISMATCH_RESTARTS"
  else
    bad "the mismatched cut cost $mis_delta restart(s), above the pinned $EXPECTED_MISMATCH_RESTARTS"
    note "this is a regression in a failure B2 does not fix but does bound: destinations"
    note "now drop MORE often at a live<->playlist cut than when the pin was measured"
  fi
fi
# The damage the restart count cannot show. One header, two geometries: this
# reports 1920x1080 (the filler's, seen first) at 60 fps (the publisher's),
# describing a file that is neither for half its length.
# AN ASSERTION, NOT A NOTE. A destination that runs its whole life and writes
# nothing is the failure this step is least able to see: the restart counter
# above reads 0 (correct -- it never restarted), every other check passes, and
# the suite goes green over a file with no bytes in it.
#
# That is not hypothetical. A guard that skipped stopDestinations left this
# destination subscribed to a hub that was closed under it; closing a hub stops
# delivery without ending the process, so FFmpeg sat there started and idle for
# 76 seconds. It reproduced about one run in two, and the only thing that showed
# it was the geometry line below coming back BLANK -- a note nobody would fail a
# build over.
# STOPPED BEFORE ITS FILE IS READ, and without this the byte count below is not
# a measurement at all.
#
# `drive stopall` runs once at step 9, hundreds of lines above, and the mismatch
# destination is added AFTER it -- deliberately, so a case that expects restarts
# cannot truncate the recording step 9 just measured. Nothing stopped it again,
# so its Matroska was still open when the size was read, and an open MKV holds
# whatever the muxer has flushed rather than what was delivered.
#
# That is not a small effect. The same healthy run reads 12845056 bytes locally
# and 0 in CI, because a flush is a function of buffer pressure and timing. The
# assertion was reading the muxer's schedule and reporting it as delivery, and
# once out_time was added the mismatch became loud: 8124ms produced against a
# 0-byte file, diagnosed as "something truncated it" when nothing had.
#
# The destinations are stopped here so the file is finalised, exactly as step 9
# does before measuring onair.mkv, and for the same reason its comment gives:
# this file was "always an unfinalised Matroska, and the duration check below was
# written around that damage instead of against it".
OUT=$(drive stopall)
case "$OUT" in *STOPPED*) : ;; *) bad "stop the destinations before measuring the mismatch file: $OUT" ;; esac
sleep 8
MIS_BYTES=$(wc -c < "$WORK/data/recordings/mismatch.mkv" 2>/dev/null | tr -d ' ')
note "mismatch produced media: settled=${mis_t_settled:--1}ms back=${mis_t_back:--1}ms; file=${MIS_BYTES:-0} bytes"
if [ "${MIS_BYTES:-0}" -gt 10000 ] 2>/dev/null; then
  ok "the mismatch destination actually wrote its output ($MIS_BYTES bytes)"
else
  # WHICH failure this is, decided by what the destination PRODUCED rather than
  # by what reached the disk. See the sampling note above and issue #275: the
  # file reads zero for the whole run even when everything is working, so it
  # cannot tell these apart and out_time can.
  #
  # -1 is not zero. It means the destination had no process to ask, which is a
  # third finding again -- and reporting it as "produced nothing" would blame
  # delivery for something that never started.
  if [ "${mis_t_back:--1}" -lt 0 ] 2>/dev/null; then
    bad "the mismatch destination had no process to report at the end of the run"
    note "so this is not a delivery failure: there was nothing running to deliver to."
  elif [ "${mis_t_back:-0}" -gt 0 ] 2>/dev/null; then
    bad "the mismatch destination produced ${mis_t_back}ms of media and its file holds ${MIS_BYTES:-0} bytes"
    note "it WAS being delivered to, so this is not the closed-hub case. A file"
    note "destination truncates and reopens when it restarts -- read the restart"
    note "count above; if that is still 0 the truncation came from outside this step."
  else
    bad "the mismatch destination produced no media at all across the whole run"
    note "out_time never moved, so nothing was reaching it -- not a muxer that had"
    note "yet to flush. It never restarted, so the count above is 0 and every other"
    note "check passed; a destination on a hub that was closed under it looks"
    note "exactly like this, and that is the 76-second case this step exists for."
  fi
fi
note "the mismatch recording declares: $(ffprobe -v error -select_streams v \
  -show_entries stream=width,height,r_frame_rate -of csv=p=0 \
  data/recordings/mismatch.mkv 2>/dev/null) -- for content that is 1280x720@60 for half its length"

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
# Fixed-value guard. Several checks live behind an "if the file exists" branch,
# so a suite that fell over early could otherwise report "all passed" having
# measured almost nothing.
#
# The filler case in steps 7 and 8 added five of these, the sequencing readback
# in step 9 three more, and the mismatch ratchet in step 10 two; every one of
# them lives after a `bad ... exit 1` precondition, so a run that could not build
# the clips or enable the tier has to be told apart from one that measured the
# case and liked what it saw.
#
# COUNTED, not estimated. A floor set below the real count is a floor a
# silently-skipped check walks straight over, which is the one thing it is here
# to stop. It is a FLOOR, not an equality: a failure that fires an extra `bad`
# on a path a clean run never takes only raises the total. If you add or remove
# a check, change this line in the same commit.
# 25 rather than 24 since #226 added the switch-count CEILING alongside its
# floor. The floor alone passed a tier switching 80 times.
#
# 27, read off a green run rather than derived. The pinned 25 was two below what
# a clean run produces, so two checks could have stopped running without this
# noticing. A floor set from arithmetic drifts away from the suite; one read off
# a green run does not.
# --- #398 RTMP ARM: the verdict -----------------------------------------------
# A NOTE, not a check: this arm is a measurement and must not move the pass/fail
# of a suite that is used as a gate elsewhere.
note ">>>RTMP final restarts=$(drive restarts rtmpdest | tail -1) outtime=$(drive outtime rtmpdest | tail -1)ms"

EXPECTED_CHECKS=27
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -eq 0 ]; then
  printf "  \033[32mFAILOVER ACCEPTANCE PASSED\033[0m\n\n"
else
  printf "  \033[31mFAILOVER ACCEPTANCE FAILED\033[0m\n\n"
fi
[ "$fail" -eq 0 ]
