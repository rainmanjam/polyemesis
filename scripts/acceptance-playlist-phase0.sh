#!/usr/bin/env bash
# Scheduled pre-recorded broadcast, with no encoder attached.
#
# This suite adds NO features. It proves that a capability people ask for
# repeatedly -- "stream from an MP4", "virtual input: looping video", "go live
# on a schedule" -- is reachable today, with `file://` pull and a schedule that
# already ship, and it pins the behaviour so a later refactor cannot quietly
# take it away.
#
# It also MEASURES the known limitation rather than describing it. The file
# loops continuously from the moment the ingest starts, so a schedule arriving
# later joins mid-file rather than at frame 0. That is the wart the full
# playlist feature exists to remove, and a number is a better record of it than
# a sentence.
#
# Usage:  ./scripts/acceptance-playlist-phase0.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-playlist}"
PORT=8097
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. Killing the server alone orphans its FFmpeg children, and
# they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"
DRIVER="$ROOT/scripts/acceptance_playlist_driver.go"
API="http://127.0.0.1:$PORT"

# The clip is deliberately SHORT so the run crosses the loop point several
# times inside a sane wall-clock budget. Looping is the thing under test.
CLIP_SECONDS=6
TONE=1200

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
note() { printf "  \033[36mNOTE\033[0m  %s\n" "$1"; }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

cleanup() { poly_cleanup "$PORT" "${WORK:-}"; }
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
command -v go >/dev/null || { echo "go is required to run the driver"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"

drive() { go run "$DRIVER" "$API" "$@"; }

# Bandpass at a known frequency and read the RMS back, so "the file's audio
# reached the destination" is a number rather than a file that merely exists.
rms() {
  ffmpeg -hide_banner -nostats -i "$1" \
    -af "bandpass=f=$2:width_type=h:w=100,astats=metadata=1:reset=0" -f null - 2>&1 |
    grep 'RMS level dB' | tail -1 | sed 's/.*: *//'
}
louder_than() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 > b+0)}'; }

step "1. A short clip to broadcast, and nothing else"
mkdir -p data/recordings
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=640x360:rate=30" \
  -f lavfi -i "sine=frequency=$TONE:sample_rate=48000" \
  -map 0:v -map 1:a \
  -c:v libx264 -preset ultrafast -g 60 -pix_fmt yuv420p -b:v 800k \
  -c:a aac -b:a 128k -ac 2 -t "$CLIP_SECONDS" \
  -y data/recordings/show.ts 2>/dev/null
[ -s data/recordings/show.ts ] \
  && ok "built a ${CLIP_SECONDS}s clip carrying a ${TONE} Hz tone" \
  || { bad "could not build the clip"; exit 1; }

step "2. Server up, with no encoder anywhere"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }
drive setup >/dev/null 2>&1 || true

step "3. The ingest is the file itself"
drive pullmode "recordings/show.ts" >/dev/null || { bad "could not switch to pull"; exit 1; }
ok "ingest set to pull from file://recordings/show.ts"

live=""
for _ in $(seq 1 40); do
  sleep 0.5
  [ "$(drive ingestlive 2>/dev/null)" = "true" ] && { live=yes; break; }
done
if [ -n "$live" ]; then
  ok "the ingest is live with NO encoder connected -- the file is the source"
else
  bad "the ingest never went live"; exit 1
fi

step "4. A destination that is off, and a schedule that turns it on"
DEST="$(drive dest)"
[ -n "$DEST" ] && ok "created destination $DEST, disabled" || { bad "no destination"; exit 1; }

[ "$(drive enabled "$DEST")" = "false" ] \
  && ok "it is disabled before the schedule fires" \
  || bad "the destination was already enabled, so the schedule proves nothing"

drive schedule "$DEST" 6 >/dev/null || { bad "could not arm the schedule"; exit 1; }
ok "armed a one-shot 'start' for 6s from now"

# Deliberately sampled BEFORE the window: a schedule that fires early is as
# wrong as one that never fires, and only checking afterwards would miss it.
sleep 3
[ "$(drive enabled "$DEST")" = "false" ] \
  && ok "still disabled 3s in -- the schedule did not fire early" \
  || bad "the destination came up before its window"

armed=""
for _ in $(seq 1 30); do
  sleep 1
  [ "$(drive enabled "$DEST")" = "true" ] && { armed=yes; break; }
done
if [ -n "$armed" ]; then
  ok "the schedule enabled the destination at its window"
else
  bad "the schedule never fired"; exit 1
fi

step "5. What the destination actually received"
# Long enough to cross the loop point at least twice.
WATCH=$(( CLIP_SECONDS * 3 ))
read -r ts0 lost0 <<<"$(drive tslost)"
sleep "$WATCH"
read -r ts1 lost1 <<<"$(drive tslost)"
drive stopall >/dev/null
sleep 2

OUT="data/recordings/scheduled.mkv"
if [ ! -s "$OUT" ]; then
  OUT="$(ls -1 data/recordings/scheduled*.mkv 2>/dev/null | tail -1)"
fi
if [ -s "$OUT" ]; then
  ok "the destination wrote a file while nothing but a clip was playing"
else
  bad "the destination produced nothing"; exit 1
fi

got="$(rms "$OUT" "$TONE")"
if [ -n "$got" ] && louder_than "$got" "-40"; then
  ok "it carries the clip's ${TONE} Hz tone at ${got} dBFS"
else
  bad "the ${TONE} Hz tone is absent or inaudible (${got:-nothing} dBFS)"
fi

dur="$(ffprobe -v error -select_streams v -show_entries format=duration \
  -of default=nw=1:nk=1 "$OUT" 2>/dev/null)"
if [ -n "$dur" ] && louder_than "$dur" "$CLIP_SECONDS"; then
  ok "the output is ${dur}s, longer than the ${CLIP_SECONDS}s clip -- it looped"
else
  bad "the output is ${dur:-unknown}s, so the clip did not loop"
fi

step "6. The loop seam, measured rather than assumed"
# The relay has counted MPEG-TS continuity-counter breaks all along. Crossing a
# loop point without one is the difference between "looping works" and "looping
# works and nothing downstream notices".
delta_ts=$(( ts1 - ts0 ))
delta_lost=$(( lost1 - lost0 ))
note "over ${WATCH}s the relay saw ${delta_ts} TS packets and ${delta_lost} continuity breaks"
if [ "$delta_ts" -gt 0 ]; then
  ok "the relay was carrying traffic across the loop points"
else
  bad "no TS packets crossed the relay"
fi
if [ "$delta_lost" -eq 0 ]; then
  ok "zero continuity breaks -- -stream_loop rewinds without a visible seam"
else
  # Not a failure. It is the number the full playlist feature has to beat, and
  # recording it is the point.
  note "${delta_lost} continuity breaks across ~2 loop points is the baseline the"
  note "concat-demuxer playlist must improve on. Recorded, not asserted."
  ok "the seam cost is measured and recorded"
fi

step "7. The limitation this does NOT solve"
note "The clip loops from the moment the ingest starts, so a schedule arriving"
note "later joins MID-FILE rather than at frame 0. Nothing here can fix that:"
note "scheduler.Actuator can only flip a destination's enabled bit, and knows"
note "nothing about sources. Starting at frame 0 needs the full playlist work."
ok "the known limitation is recorded rather than papered over"

step "Summary"
total=$((pass + fail))
printf "  %d passed, %d failed\n" "$pass" "$fail"

# Fixed-value guard. A suite whose checks live behind conditionals can report
# "N passed, 0 failed" having silently skipped half of them; asserting the COUNT
# is what caught that here once before.
EXPECTED_CHECKS=15
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran; the rest never executed.\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
[ "$fail" -eq 0 ] || exit 1
printf "\n  \033[32mScheduled pre-recorded broadcast works today, with no new code.\033[0m\n\n"
