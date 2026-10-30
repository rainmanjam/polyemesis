#!/usr/bin/env bash
# Audio-drift acceptance: a destination that rides a format-changing failover
# keeps its audio on the video's timeline, there and back.
#
# THE FAULT. The selector keeps a destination on the same hub across a switch,
# and the relay keeps the timeline moving forward (-output_ts_offset). When the
# new feed decodes at another sample rate or channel count, FFmpeg in the
# destination rebuilds the filter graph from its argv in the middle of the run.
# The routing graph ended in `aresample=...:async=1:first_pts=0`, and a rebuilt
# instance of that filter meets its first frame at T seconds -- the stream's
# runtime -- and pads T seconds of silence in front of it. The muxer clamps
# those samples onto the current timestamp, so on the wire the audio is
# suddenly T seconds behind the video, and stays there.
#
# Two destinations. "restarted" gets a live gain change first, so the engine
# restarts it at the switch (respawnStaleLevels) and it never shows the fault:
# it is the control. "live" rides the switch on one process: it is the subject.
#
# TWO MODES, because two different switches change the format:
#   DRIFT_MODE=backup (default)  48 kHz stereo primary, 44.1 kHz mono backup
#   DRIFT_MODE=slate             44.1 kHz mono primary, the 48 kHz stereo slate
# Both go out and come back (pin primary), so both rebuilds are measured.
#
# WHAT IS MEASURED, on the last session the RTMP sink recorded per destination:
#   hidden audio   decoded audio duration minus the span its timestamps cover.
#                  Padding clamped onto one timestamp is exactly this; a correct
#                  run reads <= 0 (an outage before a rebuild stays a timestamp
#                  gap, as it does on the copied video).
#   A/V offset     every encoder flashes the picture white and beeps at 3 kHz
#                  for 0.1 s every 2 s; each beep's timestamp minus the nearest
#                  flash's, as the median over the whole session. This is lip
#                  sync as a platform that honours timestamps would play it.
#   longest gap    the longest silence on the SAMPLE timeline, i.e. as heard.
# The subject must also have exactly one session at the sink.
#
# Usage:  ./scripts/acceptance-audio-drift.sh [workdir]
#   POLYEMESIS_IMAGE   image to test (default: build this checkout)
#   DRIFT_PREFIX       docker name prefix (default pe-drift-)
#   DRIFT_MODE         backup | slate (default backup)
#   DRIFT_RUNTIME      seconds on the primary before the failover (default 90)
#   DRIFT_AFTER        seconds on the failover source, and again after the
#                      return (default 30)
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
. "$SCRIPTS/lib-preflight.sh"
trap 'poly_verdict_trap $?' EXIT

P="${DRIFT_PREFIX:-pe-drift-}"
WORK="${1:-${TMPDIR:-/tmp}/polyemesis-acceptance-audio-drift}"
MODE="${DRIFT_MODE:-backup}"
RUNTIME="${DRIFT_RUNTIME:-90}"
AFTER="${DRIFT_AFTER:-30}"
# The pass lines. 100 ms is well inside what a viewer notices and two orders of
# magnitude below the fault, which is the stream's whole runtime.
MAX_MS=100
# A failover is an outage: grace plus the switch is heard as a few seconds of
# silence, and that is correct. A third of the runtime is far above it and far
# below the fault. The slate's audio IS silence, so in slate mode its time on
# air is added.
MAX_GAP_S=$(( RUNTIME / 3 ))
HTTP=18480; INGEST=19355
NET="${P}net"; SRV="${P}server"; SINK="${P}sink"

case "$MODE" in
  backup) P_RATE=48000; P_CH=2; FAILTO=backup ;;
  slate)  P_RATE=44100; P_CH=1; FAILTO=slate; MAX_GAP_S=$(( MAX_GAP_S + AFTER )) ;;
  *) poly_preflight_fail "DRIFT_MODE must be backup or slate, not '$MODE'" ;;
esac

poly_require_docker "the server and the RTMP sink run in containers"
poly_require_cmd ffmpeg "publishes the encoders and measures the recordings"
poly_require_cmd ffprobe "reads the recordings' timestamps"
poly_require_cmd go "builds the driver"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

cleanup() {
  pkill -f "drift-publisher-${P}" 2>/dev/null
  docker rm -f "$SRV" "$SINK" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
}
trap 'rc=$?; poly_verdict_trap $rc cleanup' EXIT

cleanup
command rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK" || exit 1

IMAGE="${POLYEMESIS_IMAGE:-}"
if [ -z "$IMAGE" ]; then
  IMAGE="${P}poly:local"
  docker build -t "$IMAGE" --build-arg VERSION=acceptance "$ROOT" > build.log 2>&1 \
    || poly_preflight_fail "could not build the image (see $WORK/build.log)"
fi
DRIVER="$WORK/drift-driver"
( cd "$ROOT" && go build -o "$DRIVER" "$SCRIPTS/acceptance_audio_drift_driver.go" ) \
  || poly_preflight_fail "could not build the driver"
drive() { "$DRIVER" "http://127.0.0.1:$HTTP" "$@" 2>&1; }
active() { drive status | tail -1 | awk '{print $1}'; }
wait_active() { # wait_active <kind> <seconds>
  local i
  for i in $(seq 1 "$2"); do [ "$(active)" = "$1" ] && return 0; sleep 1; done
  return 1
}

# publish <tag> <key> <audio-rate> <channels> <tone-hz>: an encoder standing in
# for OBS. Every encoder shares one video shape -- destinations copy video, and a
# codec change is a different fault -- and they differ only in their audio.
# The flash and the beep are the sync marks; the steady tone under the beep
# keeps the audio from reading as silence.
MARK='lt(mod(t,2),0.1)'
publish() {
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30,drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='$MARK'" \
    -f lavfi -i "aevalsrc='0.1*sin(2*PI*$5*t)+if($MARK,0.6*sin(2*PI*3000*t),0)':s=$3" \
    -metadata comment="drift-publisher-${P}$1" \
    -map 0:v -map 1:a -c:v libx264 -preset veryfast -profile:v high -level 4.0 \
    -g 60 -pix_fmt yuv420p -b:v 1000k -c:a aac -b:a 128k -ar "$3" -ac "$4" \
    -t 3600 -f flv "rtmp://127.0.0.1:$INGEST/live/$2" > "publisher-$1.log" 2>&1 &
}

# measure <flv>: "<hidden-ms> <av-offset-ms> <beeps> <longest-gap-s> <audio-s>"
measure() {
  local f="$1" dec a g sync
  dec=$(ffmpeg -v error -i "$f" -map 0:a:0 -ac 1 -ar 48000 -f s16le - 2>/dev/null \
      | wc -c | awk '{printf "%.3f", $1/(2*48000)}')
  a=$(ffprobe -v error -select_streams a:0 -show_entries packet=pts_time -of csv=p=0 "$f" \
      | awk 'NR==1{f=$1} {l=$1} END{printf "%s %s", f, l}')
  g=$(ffmpeg -hide_banner -nostats -i "$f" -map 0:a:0 \
      -af "asetpts=N/SR/TB,silencedetect=n=-60dB:d=0.5" -f null - 2>&1 \
      | awk -F'silence_duration: ' '/silence_duration/{if($2>m)m=$2} END{printf "%.3f", m+0}')
  # Beep onsets on the audio's timestamps, flash onsets on the video's. The
  # flash is found as black after negating the picture.
  ffmpeg -hide_banner -nostats -i "$f" -map 0:a:0 \
    -af "highpass=f=2500,highpass=f=2500,highpass=f=2500,silencedetect=n=-30dB:d=0.04" -f null - 2>&1 \
    | grep -o "silence_end: [0-9.]*" | awk '{print $2}' > "$f.beeps"
  ffmpeg -hide_banner -nostats -i "$f" -map 0:v:0 -an \
    -vf "negate,blackdetect=d=0.03:pix_th=0.1:pic_th=0.9" -f null - 2>&1 \
    | grep -o "black_start:[0-9.]*" | cut -d: -f2 > "$f.flashes"
  sync=$(awk 'NR==FNR{fl[n++]=$1; next} {b=1e9; for(i=0;i<n;i++){d=$1-fl[i]; if((d<0?-d:d)<(b<0?-b:b)) b=d}
              if(b<1 && b>-1) printf "%d\n", b*1000}' "$f.flashes" "$f.beeps" \
      | sort -n | awk '{v[NR]=$1} END{if(!NR){print "99999 0"; exit} printf "%d %d", v[int((NR+1)/2)], NR}')
  set -- $a
  awk -v d="$dec" -v af="$1" -v al="$2" -v g="$g" -v s="$sync" 'BEGIN{
    printf "%d %s %s %s\n", (d - (al - af + 1024/48000))*1000, s, g, d }'
}

step "1. Server and an RTMP sink, in containers ($MODE mode)"
docker network create "$NET" >/dev/null
docker run -d --name "$SRV" --network "$NET" -e POLYEMESIS_SETUP_CODE \
  -p "$HTTP:8080" -p "$INGEST:1935" "$IMAGE" >/dev/null \
  || { bad "server container did not start"; exit 1; }
# The sink: one FFmpeg listener per destination, relaunched whenever its
# publisher disconnects, each session written to its own numbered file. A
# destination that was restarted leaves several files; one that rode every
# switch leaves one.
docker run -d --name "$SINK" --network "$NET" --user 0:0 --entrypoint sh "$IMAGE" -c '
  mkdir -p /sink
  for d in restarted:1936 live:1937; do
    (n=0; while true; do
       ffmpeg -hide_banner -loglevel error -listen 1 -i "rtmp://0.0.0.0:${d#*:}/live/${d%%:*}" \
         -c copy -f flv "/sink/${d%%:*}-$n.flv"; n=$((n+1)); done) &
  done
  wait' >/dev/null || { bad "sink container did not start"; exit 1; }
OUT=$(drive setup "$MODE")
case "$OUT" in *FAILOVER_OK*) ok "failover onto the $FAILTO is enabled" ;;
  *) bad "setup: $OUT"; exit 1 ;; esac
KEY=$(drive publishkey | tail -1)
[ -n "$KEY" ] || { bad "no publish key"; exit 1; }

step "2. Primary at $P_RATE Hz x$P_CH; the $FAILTO decodes differently"
publish primary "$KEY" "$P_RATE" "$P_CH" 1000
[ "$MODE" = backup ] && publish backup "$KEY.backup" 44100 1 440
wait_active primary 60 && ok "the primary is on air" || { bad "the primary never went on air"; exit 1; }

for d in restarted:1936 live:1937; do
  drive adddest "${d%%:*}" "rtmp://$SINK:${d#*:}/live" | grep -q DEST_OK \
    || { bad "could not add destination ${d%%:*}"; exit 1; }
done
running=no
for _ in $(seq 1 90); do
  if [ "$(drive restarts restarted | tail -1)" != "-1" ] && [ "$(drive restarts live | tail -1)" != "-1" ]; then
    running=yes; break
  fi
  sleep 1
done
[ "$running" = yes ] && ok "both destinations are publishing" || { bad "destinations never started"; exit 1; }
sleep 3
# A live level change on the control only. It is applied over the command
# channel without a restart, which leaves the argv carrying the old level, so
# the engine restarts this destination at the next feed change.
drive gain restarted 0.5 | grep -q GAIN_OK && ok "live gain change applied to the control" \
  || bad "the live gain change was refused"

step "3. ${RUNTIME}s on the primary, then the primary disappears"
sleep "$RUNTIME"
pkill -f "drift-publisher-${P}primary"
wait_active "$FAILTO" 30 && ok "the $FAILTO took over" || bad "the selector never moved to the $FAILTO"
sleep "$AFTER"

step "4. The primary returns, in its own format"
publish primary "$KEY" "$P_RATE" "$P_CH" 1000
sleep 8
drive pin primary | grep -q PIN_OK || bad "could not put the primary back on air"
wait_active primary 30 && ok "the primary is back on air" || bad "the primary never came back"
sleep "$AFTER"
# Stopped through the API first, so each destination's FFmpeg flushes its
# encoder and its last packets reach the sink rather than being cut off.
drive stopall | grep -q STOPPED || bad "could not stop the destinations cleanly"
sleep 3
pkill -f "drift-publisher-${P}" 2>/dev/null
docker rm -f "$SRV" >/dev/null 2>&1
sleep 2
docker cp "$SINK:/sink" "$WORK/sink" >/dev/null 2>&1

step "5. THE POINT: audio is still on the video's timeline"
for name in live restarted; do
  last=$(ls "$WORK/sink/$name"-*.flv 2>/dev/null | sort -t- -k2 -n | tail -1)
  segs=$(ls "$WORK/sink/$name"-*.flv 2>/dev/null | wc -l | tr -d ' ')
  [ -n "$last" ] || { bad "$name: the sink recorded nothing"; continue; }
  read -r hidden avoff beeps gap dec < <(measure "$last")
  note "$name: $segs session(s); last: ${dec}s of audio, hidden ${hidden} ms, A/V offset ${avoff} ms over $beeps beeps, longest silence ${gap}s"
  if [ "$name" = live ]; then
    [ "$segs" = 1 ] && ok "live: one unbroken session at the sink (the subject rode both switches)" \
      || bad "live: $segs sessions at the sink, so it was restarted and measures nothing"
  else
    # The restart counter does not move on a respawnStaleLevels restart, so the
    # sink's session count is the evidence.
    [ "$segs" -gt 1 ] && ok "restarted: reconnected at the switch ($segs sessions), as a control should" \
      || bad "restarted: only $segs session; the control was never restarted"
  fi
  [ "$hidden" -lt "$MAX_MS" ] && ok "$name: no audio hidden behind the timestamps (${hidden} ms)" \
    || bad "$name: ${hidden} ms of audio squeezed behind the timestamps"
  [ "${avoff#-}" -lt "$MAX_MS" ] && [ "$beeps" -ge 10 ] \
    && ok "$name: audio within ${MAX_MS} ms of the video (median ${avoff} ms over $beeps marks)" \
    || bad "$name: audio ${avoff} ms off the video ($beeps marks matched)"
  awk -v g="$gap" -v m="$MAX_GAP_S" 'BEGIN{exit !(g < m)}' \
    && ok "$name: no runtime-length silence (longest ${gap}s)" \
    || bad "$name: ${gap}s of silence, against a ${RUNTIME}s runtime"
done

step "Summary"
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
