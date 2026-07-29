#!/usr/bin/env bash
# End-state acceptance test for per-destination video renditions.
#
# scripts/acceptance.sh proves the product's differentiator — per-destination
# audio routing. This one proves renditions were added on top of it WITHOUT
# taking it away:
#
#   1080p60 3-tone ingest
#     ├─ dest "passthrough"  -c:v copy of the SOURCE   tracks 1+2
#     └─ rendition "720p30"  ONE shared video encode, all audio copied
#          ├─ dest A         -c:v copy of the ENCODE   tracks 1+3
#          └─ dest B         -c:v copy of the ENCODE   tracks 2+3
#
# and asserts, by measurement:
#   - the passthrough output is still 1920x1080 at 60 fps;
#   - the rendition outputs are 1280x720 (or 720x1280) at 30 fps;
#   - all three carry EXACTLY their selected tones, by per-band RMS energy —
#     the same measurement acceptance.sh uses, because the whole risk of this
#     feature is that a shared video encode quietly flattens the audio;
#   - ONE encoder process served both rendition destinations (ref counting up),
#     and it went away when the last one was stopped (ref counting down).
#
# Usage:  ./scripts/acceptance-renditions.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-renditions}"
PORT=8099
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup "$PORT" "${WORK:-}"
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"

# ---------------------------------------------------------------- 1. server
step "1. Start the binary"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do
  sleep 0.3
  if grep -q "web ui" server.log 2>/dev/null; then break; fi
done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || bad "server did not start"

# The ingest relay hub binds a random loopback UDP port. Taken now, before any
# rendition exists, so it cannot be confused with a rendition's own hub.
SRVPID=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
RELAY=$(lsof -nP -iUDP -a -p "$SRVPID" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}')
[ -n "$RELAY" ] && ok "ingest relay hub bound (udp/$RELAY)" || bad "no relay port"

# --------------------------------------------------------------- 2. the app
step "2. Setup, rendition and destinations (via the API the UI uses)"
FACTS="$WORK/facts.env"
go run "$SCRIPTS/acceptance_renditions_driver.go" "$PORT" "$RELAY" "$FACTS" 2>&1 | sed 's/^/  /'

[ -s "$FACTS" ] || { bad "driver wrote no facts"; step "Summary"; printf "  %d passed, %d failed\n\n" "$pass" "$fail"; exit 1; }
# shellcheck disable=SC1090
source "$FACTS"

if [ -n "${DRIVER_FAILED:-}" ]; then bad "driver aborted: $DRIVER_FAILED"; fi

# ------------------------------------------------------------- 3. verify A/V
step "3. Verify video: passthrough keeps the source, the rendition steps it down"

vprop() { # vprop <file> <entry>
  ffprobe -v error -select_streams v:0 -show_entries "stream=$2" -of csv=p=0 "$1"
}

fps_of() { # fps_of <file> -> integer frames per second, rounded
  local r; r=$(vprop "$1" avg_frame_rate)
  [ -z "$r" ] || [ "$r" = "0/0" ] && r=$(vprop "$1" r_frame_rate)
  awk -F/ -v r="$r" 'BEGIN{split(r,a,"/"); if (a[2]+0==0) print 0; else printf "%.0f", a[1]/a[2]}'
}

check_video() { # check_video <file> <label> <wantW> <wantH> <wantFPS>
  local f="$1" label="$2" ww="$3" wh="$4" wf="$5"
  if [ ! -s "$f" ]; then bad "$label: output file missing or empty"; return 1; fi

  local w h fps; w=$(vprop "$f" width); h=$(vprop "$f" height); fps=$(fps_of "$f")

  # Either orientation counts as the requested frame size: nothing in the
  # pipeline rotates, but pinning only one order would fail for the wrong
  # reason on a portrait source.
  if { [ "$w" = "$ww" ] && [ "$h" = "$wh" ]; } || { [ "$w" = "$wh" ] && [ "$h" = "$ww" ]; }; then
    ok "$label: video is ${w}x${h} (expected ${ww}x${wh})"
  else
    bad "$label: video is ${w}x${h}, expected ${ww}x${wh}"
  fi

  # One frame of tolerance: a live encode's average rate is not exact.
  if [ -n "$fps" ] && [ "$fps" -ge "$((wf - 1))" ] && [ "$fps" -le "$((wf + 1))" ]; then
    ok "$label: video is ${fps} fps (expected ${wf})"
  else
    bad "$label: video is '${fps}' fps, expected ${wf}"
  fi
}

OUT=data/recordings
check_video "$OUT/passthrough.mkv"  "passthrough"  1920 1080 60
check_video "$OUT/rendition-a.mkv"  "720p30 dest A" 1280 720 30
check_video "$OUT/rendition-b.mkv"  "720p30 dest B" 1280 720 30

step "3b. Verify burned-in text actually reached the pixels"

# Measured, not asserted. drawtext that renders nothing EXITS 0 -- proven
# earlier by removing expansion=none, where "100% LIVE" drew zero pixels and
# FFmpeg reported success -- so nothing about the process's exit status or the
# stored settings can tell you whether the caption is on screen.
#
# The rendition draws a WHITE box at full opacity across the top-left 12% of
# the frame. testsrc2 puts a colour-bar field there, so a crop of that corner
# is bright ONLY if the box rendered.
mean_luma() { # mean_luma <file> <crop>  -> 0-255, or empty
  # -v info, NOT -v error. `metadata=print` writes at INFO level, so -v error
  # suppresses the one line this parses and the function returns nothing --
  # which reads as "the caption did not render" and is wrong. The band()
  # helper below uses -v info for exactly the same reason.
  #
  # Measured: on a white frame this prints 255 and on a black one 0; with
  # -v error both print nothing at all.
  ffmpeg -v info -i "$1" -vf "crop=$2,format=gray,signalstats,metadata=print:key=lavfi.signalstats.YAVG" \
    -frames:v 1 -f null - 2>&1 | awk -F= '/YAVG/{print int($NF)}' | tail -1
}

if [ "${TEXT_SUPPORTED:-no}" != "yes" ]; then
  # Reported rather than silently skipped, and NOT counted as a pass: an
  # FFmpeg without libfreetype has no drawtext filter at all, and a green run
  # that quietly checked nothing is the outcome this suite exists to prevent.
  printf "  \033[33m--\033[0m text: this FFmpeg has no drawtext filter, so the pixel check cannot run\n"
else
  [ "${TEXT_CONTENT_STORED:-}" = "POLYEMESIS" ] \
    && ok "text round-tripped through the store" \
    || bad "text content came back as '${TEXT_CONTENT_STORED:-}'"
  [ "${TEXT_BOX_STORED:-no}" = "yes" ] \
    && ok "the text box flag survived the store" \
    || bad "the box flag came back as '${TEXT_BOX_STORED:-}'"
  [ "${FONT_COUNT:-0}" -ge 2 ] \
    && ok "the fonts endpoint lists ${FONT_COUNT} fonts on disk" \
    || bad "the fonts endpoint listed ${FONT_COUNT:-0} fonts; the embedded ones were not written out"

  # Top-left 20% x 15%, comfortably inside a box drawn at 12% of height.
  TEXT_LUMA=$(mean_luma "$OUT/rendition-a.mkv" "iw*0.2:ih*0.15:0:0")
  BASE_LUMA=$(mean_luma "$OUT/passthrough.mkv" "iw*0.2:ih*0.15:0:0")
  if [ -n "$TEXT_LUMA" ] && [ "$TEXT_LUMA" -ge 170 ]; then
    ok "the text box is on screen (top-left luma ${TEXT_LUMA}, passthrough ${BASE_LUMA:-?})"
  else
    bad "top-left luma is ${TEXT_LUMA:-?} against a passthrough ${BASE_LUMA:-?}; the caption did not render"
  fi
  # The control. Without it a source that happened to be white everywhere
  # would pass the check above while proving nothing.
  if [ -n "$BASE_LUMA" ] && [ -n "$TEXT_LUMA" ] && [ "$TEXT_LUMA" -gt "$BASE_LUMA" ]; then
    ok "the same corner is darker without the overlay (${BASE_LUMA} vs ${TEXT_LUMA})"
  else
    bad "passthrough luma ${BASE_LUMA:-?} is not below the rendition's ${TEXT_LUMA:-?}, so the measurement proves nothing"
  fi

  # The passthrough must be UNTOUCHED. Text lives on a rendition; a
  # destination doing -c:v copy has no mechanism by which a copied bitstream
  # acquires a caption, and if it ever did the product's central promise
  # would be broken.
  ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
    -of default=nw=1:nk=1 "$OUT/passthrough.mkv" >/dev/null 2>&1 \
    && ok "the passthrough destination still decodes as video" \
    || bad "the passthrough output is unreadable"
fi

step "4. Verify audio routing survived the shared video encode"

band() {  # band <file> <freq>  -> RMS dBFS in a narrow band
  ffmpeg -v info -i "$1" \
    -af "bandpass=frequency=$2:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
}

check_audio() { # check_audio <file> <label> <present300> <present900> <present2000>
  local f="$1" label="$2"; shift 2
  if [ ! -s "$f" ]; then bad "$label: output file missing or empty"; return; fi

  # Exactly one stereo AAC stream: the rendition copied every track through, and
  # the DESTINATION mixed them down. If the rendition had touched audio, this is
  # where it would show up.
  local streams n
  streams=$(ffprobe -v error -select_streams a -show_entries stream=codec_name,channels \
            -of csv=p=0 "$f" | tr '\n' ' ')
  n=$(echo "$streams" | wc -w | tr -d ' ')
  if [ "$n" = "1" ]; then ok "$label: exactly one audio stream ($streams)"
  else bad "$label: expected 1 audio stream, got '$streams'"; fi

  local i=1
  for freq in 300 900 2000; do
    local want db present
    want=$(eval echo "\${$i}"); i=$((i+1))
    db=$(band "$f" "$freq")
    # A present tone sits near -21 dB; an excluded one is 28-37 dB lower, at the
    # bandpass filter's leakage floor. -35 dB separates them with margin.
    present=$(awk -v d="$db" 'BEGIN{print (d > -35) ? "yes" : "no"}')
    if [ "$present" = "$want" ]; then
      ok "$label: ${freq}Hz ${db}dB present=$present (expected $want)"
    else
      bad "$label: ${freq}Hz ${db}dB present=$present but expected $want"
    fi
  done
}

check_audio "$OUT/passthrough.mkv"  "passthrough (tracks 1+2)"  yes yes no
check_audio "$OUT/rendition-a.mkv"  "720p30 dest A (tracks 1+3)" yes no  yes
check_audio "$OUT/rendition-b.mkv"  "720p30 dest B (tracks 2+3)" no  yes yes

step "5. Verify the encode is SHARED, not per destination"

[ "${PROCS_BEFORE_SELECT:-x}" = "0" ] \
  && ok "a rendition nothing selects burns no CPU (0 encoder processes)" \
  || bad "rendition with no destinations had ${PROCS_BEFORE_SELECT:-?} encoder processes, expected 0"

[ "${CONSUMERS:-0}" = "2" ] \
  && ok "the rendition reports 2 consumers" \
  || bad "rendition reports ${CONSUMERS:-?} consumers, expected 2"

[ "${RENDITION_RUNNING:-no}" = "yes" ] \
  && ok "the rendition encoder is running" \
  || bad "the rendition encoder is not running (error: ${RENDITION_ERROR:-none})"

# The assertion the whole design rests on: two destinations, ONE encode.
if [ "${PROCS_MAX:-0}" = "1" ] && [ "${PROCS_MIN:-0}" = "1" ]; then
  ok "exactly ONE encoder process served both destinations for the whole run"
else
  bad "encoder processes ranged ${PROCS_MIN:-?}..${PROCS_MAX:-?} over the run, expected exactly 1"
fi

[ "${RELAY_PORT:-0}" != "0" ] && [ "${RELAY_PORT:-0}" != "${INGEST_RELAY_PORT:-0}" ] \
  && ok "the rendition publishes to its own hub (udp/${RELAY_PORT}), not the ingest's (udp/${INGEST_RELAY_PORT})" \
  || bad "rendition hub port '${RELAY_PORT:-?}' vs ingest '${INGEST_RELAY_PORT:-?}': expected a distinct hub"

[ "${PROCS_AFTER_RELEASE:-1}" = "0" ] \
  && ok "the encoder stopped when the last destination released it" \
  || bad "${PROCS_AFTER_RELEASE:-?} encoder processes still running after the last release, expected 0"

[ "${CONSUMERS_AFTER_RELEASE:-1}" = "0" ] && [ "${RENDITION_RUNNING_AFTER_RELEASE:-yes}" = "no" ] \
  && ok "the idle rendition reports 0 consumers and no process" \
  || bad "after release: consumers=${CONSUMERS_AFTER_RELEASE:-?} running=${RENDITION_RUNNING_AFTER_RELEASE:-?}"

step "6. Verify a passthrough destination is unchanged for existing installs"

[ "${PASSTHROUGH_HAS_RENDITION_KEY:-yes}" = "no" ] \
  && ok "a passthrough destination omits renditionId entirely" \
  || bad "a passthrough destination sent a renditionId key; clients must read absent as passthrough"

[ -n "${DISCLAIMER:-}" ] && case "$DISCLAIMER" in
  *"verify current limits with the platform"*)
    ok "presets ship the platform-limits disclaimer verbatim" ;;
  *) bad "preset disclaimer is '$DISCLAIMER'" ;;
esac || bad "presets shipped no disclaimer"

# ------------------------------------------------------------------ summary
step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
echo "  RENDITION ACCEPTANCE PASSED"
