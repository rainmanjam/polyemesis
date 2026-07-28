#!/usr/bin/env bash
# Synthetic-source acceptance: silence-on-video-only, and the failover slate.
#
# Both exist for the moment things are going wrong, which is exactly when nobody
# is watching a test suite. A video-only ingest is refused by every major
# platform, so the silence tier is what stops a destination either failing to
# compile or crash-looping on an audio track that is not there; the slate is
# what a viewer sees instead of a frozen frame when the encoder disappears.
#
# Neither had an end-to-end proof. Both were unit-tested against fakes.
#
# Usage:  ./scripts/acceptance-synth.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-synth}"
PORT=8092
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

cleanup() { poly_cleanup "$PORT" "$WORK"; }
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
mkdir -p data/recordings

drive() { go run "$SCRIPTS/acceptance_synth_driver.go" "http://127.0.0.1:$PORT" "$@" 2>&1; }

step "1. A VIDEO-ONLY source, which every platform refuses"
# No -map for audio at all: this is the case the silence tier exists for.
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=640x360:rate=30" \
  -map 0:v -c:v libx264 -preset ultrafast -g 60 -pix_fmt yuv420p -b:v 1000k \
  -t 90 -y data/recordings/videoonly.ts 2>/dev/null
tracks=$(ffprobe -v error -select_streams a -show_entries stream=index -of csv=p=0 data/recordings/videoonly.ts | grep -c . || true)
if [ -s data/recordings/videoonly.ts ] && [ "$tracks" -eq 0 ]; then
  ok "built a source with zero audio tracks"
else
  bad "could not build a video-only source (found $tracks audio tracks)"; exit 1
fi

step "2. Server"
"$BIN" -addr ":$PORT" -data ./data -log debug > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }

OUT=$(drive)
case "$OUT" in *SETUP_OK*) ok "first-run setup" ;; *) bad "setup: $OUT"; exit 1 ;; esac
case "$OUT" in *PULL_OK*)  ok "ingest dialling the video-only source" ;; *) bad "pull: $OUT"; exit 1 ;; esac
case "$OUT" in *DEST_OK*)  ok "one destination, selecting track 1" ;; *) bad "dest: $OUT"; exit 1 ;; esac

step "3. The silence tier makes a video-only ingest usable"
# Without it, a destination selecting track 1 has no track 1 to select: it
# either refuses to compile or crash-loops. With it, there is a silent stereo
# track to route.
probed=no
for _ in $(seq 1 40); do
  n=$(drive tracks | tail -1)
  if [ "${n:-0}" -ge 1 ] 2>/dev/null; then probed=yes; break; fi
  sleep 1
done
if [ "$probed" = yes ]; then
  ok "a synthetic audio track is present on a video-only ingest"
else
  bad "no audio track appeared; a destination here would crash-loop"
fi

sleep 25
drive stopall >/dev/null 2>&1
sleep 8

if [ -s data/recordings/synth.mkv ]; then
  ok "the destination produced an output rather than crash-looping"
  a=$(ffprobe -v error -select_streams a -show_entries stream=index -of csv=p=0 data/recordings/synth.mkv | grep -c . || true)
  [ "${a:-0}" -ge 1 ] && ok "the output carries an audio track" \
                      || bad "the output has no audio, which every platform refuses"
  # It must be SILENT, not noise: a synthesised track that is not silent would
  # be audible garbage on air.
  rms=$(ffmpeg -hide_banner -nostats -i data/recordings/synth.mkv \
        -af "astats=metadata=1:reset=0" -f null - 2>&1 |
        grep 'RMS level dB' | tail -1 | sed 's/.*: *//')
  if awk -v r="$rms" 'BEGIN{exit !(r == "-inf" || r+0 < -60)}'; then
    ok "the synthetic track is silent (RMS $rms dB)"
  else
    bad "the synthetic track is not silent (RMS $rms dB) — that is audible on air"
  fi
else
  bad "no output was produced"
fi

step "4. The slate is refused when its image is unusable"
# Confinement, same as a pull source: the slate path is a file this process
# opens, so it must not be able to point outside the data directory.
OUT=$(drive slate-escape)
case "$OUT" in
  *SLATE_REFUSED*) ok "a slate image outside the data directory is refused" ;;
  *)               bad "an escaping slate path was accepted: $OUT" ;;
esac

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
EXPECTED_CHECKS=10
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -eq 0 ]; then
  printf "  \033[32mSYNTH ACCEPTANCE PASSED\033[0m\n\n"
else
  printf "  \033[31mSYNTH ACCEPTANCE FAILED\033[0m\n\n"
fi
[ "$fail" -eq 0 ]
