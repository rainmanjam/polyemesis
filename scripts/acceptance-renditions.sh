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
#     and it went away when the last one was stopped (ref counting down);
#   - a rendition storing an explicit maxrate/bufsize started an FFmpeg whose
#     command line carries THOSE numbers rather than the CBR pair derived from
#     the target bitrate. A second rendition exists solely to carry them.
#
# Usage:  ./scripts/acceptance-renditions.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-renditions}"
PORT=8099
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
# A deadline of our own. See lib-watchdog.sh: the job ceiling cancels a hung
# suite and prints nothing, so the suite has to give up first and say what it
# was waiting for.
. "$SCRIPTS/lib-watchdog.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"
}
trap 'poly_teardown_trap $? cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm

# A watermark for the image-overlay checks, written before the server starts so
# the path exists the moment the rendition references it.
#
# Solid WHITE at 20% of the frame, because both properties under test are
# measured as luma against a testsrc2 background that averages far below it:
# a bright crop in the anchored corner proves position, and HOW bright proves
# the opacity was applied rather than ignored.
mkdir -p data/overlays
ffmpeg -hide_banner -loglevel error -f lavfi -i "color=c=white:s=200x200:d=1" \
  -frames:v 1 -y data/overlays/logo.png

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
check_video "$OUT/capped.mkv"       "480p30 capped" 854 480 30

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
  # A DIFFERENCE, not an absolute level, and that choice is the whole point.
  #
  # The first version of this check demanded luma >= 170 and CI measured 180 --
  # a ten-point margin on a number that legitimately moves, because font
  # rasterisation differs between FreeType versions and the crop covers more
  # area than the box does. That is a flake waiting for an FFmpeg bump.
  #
  # The signal is that the corner got BRIGHTER, and the measured gap is large:
  # 84 without the box, 180 with it. Requiring 40 sits comfortably inside that
  # while still failing outright if nothing was drawn, and it survives a change
  # to the test pattern that an absolute threshold would not.
  if [ -z "$TEXT_LUMA" ] || [ -z "$BASE_LUMA" ]; then
    # Named separately from a failed comparison. An empty reading means the
    # measurement itself broke -- which is exactly how this check first failed,
    # reporting "the caption did not render" when the truth was that ffmpeg had
    # printed nothing for it to parse.
    bad "the luma measurement produced no reading (text='${TEXT_LUMA:-}' passthrough='${BASE_LUMA:-}'); the check is broken, not the caption"
  elif [ "$((TEXT_LUMA - BASE_LUMA))" -ge 40 ]; then
    ok "the text box is on screen (top-left luma ${TEXT_LUMA} against ${BASE_LUMA} without it)"
  else
    bad "top-left luma ${TEXT_LUMA} is not meaningfully above the passthrough's ${BASE_LUMA}; the caption did not render"
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

step "3c. Verify the image watermark landed where it was asked to"

# Overlays v0.5 shipped an image watermark with NO end-to-end coverage, and the
# text checks above did not close that gap: both compile through overlayGraph,
# but only the image exercises the second input, eof_action=repeat and the
# alpha stage.
#
# Two properties, each with a control that fails if the other explanation is
# true. A bright bottom-right corner alone would also pass if the filter
# painted the whole frame; a dark top-right alone would pass if nothing
# rendered. Together they mean the anchor was honoured.
[ "${OVERLAY_IMAGE_STORED:-}" = "overlays/logo.png" ] \
  && ok "the overlay path round-tripped through the store" \
  || bad "overlay image came back as '${OVERLAY_IMAGE_STORED:-}'"
[ "${OVERLAY_ANCHOR_STORED:-}" = "bottom-right" ] \
  && ok "the overlay anchor survived the store" \
  || bad "overlay anchor came back as '${OVERLAY_ANCHOR_STORED:-}'"

# Bottom-right 20%x25%, inside a logo drawn at 20% of the width in that corner.
LOGO_LUMA=$(mean_luma "$OUT/rendition-a.mkv" "iw*0.2:ih*0.25:iw*0.8:ih*0.75")
# The SAME crop of the passthrough, which carries no overlay at all.
LOGO_BASE=$(mean_luma "$OUT/passthrough.mkv" "iw*0.2:ih*0.25:iw*0.8:ih*0.75")
# The opposite corner of the SAME rendition. No logo there, and no text either.
CLEAN_LUMA=$(mean_luma "$OUT/rendition-a.mkv" "iw*0.2:ih*0.25:iw*0.8:0")

if [ -z "$LOGO_LUMA" ] || [ -z "$LOGO_BASE" ] || [ -z "$CLEAN_LUMA" ]; then
  bad "the overlay luma measurement produced no reading (logo='${LOGO_LUMA:-}' base='${LOGO_BASE:-}' clean='${CLEAN_LUMA:-}'); the check is broken, not the overlay"
else
  if [ "$((LOGO_LUMA - LOGO_BASE))" -ge 30 ]; then
    ok "the watermark is in the bottom-right (luma ${LOGO_LUMA} against ${LOGO_BASE} without it)"
  else
    bad "bottom-right luma ${LOGO_LUMA} is not above the passthrough's ${LOGO_BASE}; the watermark did not render"
  fi

  # The anchor, proven by absence. If this corner is as bright as the logo
  # corner, the overlay is not anchored -- it is everywhere.
  if [ "$((LOGO_LUMA - CLEAN_LUMA))" -ge 30 ]; then
    ok "the opposite corner is clean, so the anchor was honoured (${CLEAN_LUMA} vs ${LOGO_LUMA})"
  else
    bad "top-right luma ${CLEAN_LUMA} is as bright as the anchored corner ${LOGO_LUMA}; the overlay ignored its anchor"
  fi

  # Opacity, asserted against the value the operator ASKED for rather than
  # merely "not opaque".
  #
  # A white logo at alpha a over a background b composites to a*255+(1-a)*b, so
  # 50% predicts the midpoint. Checking only "below 255" would pass at 90%
  # opacity and at 10%; checking the midpoint proves the requested alpha
  # actually reached colourchannelmixer.
  #
  # This is worth pinning because the alpha stage is a DIFFERENT filter graph:
  # colourchannelmixer is omitted entirely at 100%, so no opaque overlay ever
  # exercises it and a wrong alpha would ship unnoticed.
  WANT=$(( (255 + LOGO_BASE) / 2 ))
  DIFF=$(( LOGO_LUMA > WANT ? LOGO_LUMA - WANT : WANT - LOGO_LUMA ))
  # 15 of tolerance: the composite is deterministic -- no font rasterisation is
  # involved -- so the only spread is encoder rounding on a lossy tier.
  if [ "$DIFF" -le 15 ]; then
    ok "the watermark is composited at the requested 50% (luma ${LOGO_LUMA}, predicted ${WANT} over a ${LOGO_BASE} background)"
  else
    bad "bottom-right luma ${LOGO_LUMA} is ${DIFF} away from the ${WANT} a 50% white logo predicts over ${LOGO_BASE}; the opacity is not the one that was set"
  fi
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

step "5b. Verify a capped rate control reaches the LIVE encoder"

# #341 made maxrate and bufsize settable on a rendition. Nothing was broken when
# it landed -- ffmpeg.RenditionSpec already had the fields and RenditionArgs
# already emitted them -- what was missing was the two lines of renditionSpecOf
# that read them off the stored row. A capability the code described and the
# product did not have.
#
# internal/engine/rendition_ratecontrol_test.go covers that mapping, and it
# cannot cover this: it calls renditionSpecOf directly with a db.Rendition it
# built itself, so it proves the function maps what it is handed. It cannot
# prove the API stored what was sent, that the engine read the row it stored,
# or that the process the engine spawned was started from that spec. Those are
# three separate joins, and a break in any of them looks exactly like #341 did
# -- correct on both sides, wrong in the composition.
#
# So the numbers below are read off argv of a RUNNING FFmpeg, by the driver,
# out of the process table.
#
# 4500/8000/12000 is chosen so no wrong answer can pass. RenditionArgs derives
# maxrate=target and bufsize=2x when the fields are absent, which for a 4500
# target is 4500k/9000k -- the exact pair the command line carried before #341
# regardless of what the operator stored. Those two values are called out by
# name below, because "the field never arrived" is a different bug from "the
# field arrived wrong" and the operator needs to be told which.

[ "${CAPPED_MAXRATE_STORED:-0}" = "8000" ] && [ "${CAPPED_BUFSIZE_STORED:-0}" = "12000" ] \
  && ok "the rate-control pair round-tripped through the store (maxrate ${CAPPED_MAXRATE_STORED}, bufsize ${CAPPED_BUFSIZE_STORED})" \
  || bad "the store returned maxrate='${CAPPED_MAXRATE_STORED:-}' bufsize='${CAPPED_BUFSIZE_STORED:-}', expected 8000 and 12000; the argv checks below cannot mean anything until this does"

if [ "${CAPPED_ARGV_FOUND:-no}" != "yes" ]; then
  # Named separately from a wrong value, and deliberately so. An unreadable
  # process table is a broken MEASUREMENT; reporting it as "the rate control did
  # not reach the encoder" would send someone to look at the wrong code.
  bad "no running FFmpeg carried the capped rendition's scale filter, so its command line could not be read; the measurement is broken, not necessarily the rate control"
else
  [ "${CAPPED_ARGV_BV:-}" = "4500k" ] \
    && ok "the live encoder targets -b:v 4500k" \
    || bad "the live encoder targets -b:v '${CAPPED_ARGV_BV:-}', expected 4500k"

  case "${CAPPED_ARGV_MAXRATE:-}" in
    8000k) ok "the live encoder was started with -maxrate 8000k, the stored ceiling" ;;
    4500k) bad "the live encoder was started with -maxrate 4500k -- the CBR value RenditionArgs derives from the target when the field is absent. The stored 8000 never reached the process" ;;
    *)     bad "the live encoder was started with -maxrate '${CAPPED_ARGV_MAXRATE:-}', expected the stored 8000k" ;;
  esac

  case "${CAPPED_ARGV_BUFSIZE:-}" in
    12000k) ok "the live encoder was started with -bufsize 12000k, the stored buffer" ;;
    9000k)  bad "the live encoder was started with -bufsize 9000k -- twice the target, which is what RenditionArgs derives when the field is absent. The stored 12000 never reached the process" ;;
    *)      bad "the live encoder was started with -bufsize '${CAPPED_ARGV_BUFSIZE:-}', expected the stored 12000k" ;;
  esac
fi

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
