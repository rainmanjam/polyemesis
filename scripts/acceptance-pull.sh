#!/usr/bin/env bash
# Pull-ingest acceptance.
#
# Pull is a whole ingest mode -- polyemesis DIALS a source instead of waiting
# for an encoder -- and it had unit tests but no end-to-end proof. It is what an
# IP camera, another server's HLS, or a looped file goes through, so "it
# probably works" was carrying real weight.
#
# The shape mirrors acceptance.sh deliberately: build a multitrack source, point
# the ingest at it, give two destinations different track selections, and prove
# by MEASUREMENT that each output carries exactly its own mix. The only thing
# under test that differs is how the bytes arrive.
#
# Usage:  ./scripts/acceptance-pull.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-pull}"
PORT=8094
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

cleanup() { poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"; }
trap 'poly_teardown_trap $? cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm

# The measurement helper, identical in spirit to the other suites: bandpass at a
# known frequency and read the RMS back, so "this track arrived" is a number.
rms() {
  ffmpeg -hide_banner -nostats -i "$1" \
    -af "bandpass=f=$2:width_type=h:w=$3,astats=metadata=1:reset=0" -f null - 2>&1 |
    grep 'RMS level dB' | tail -1 | sed 's/.*: *//'
}
louder_than() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 > b+0)}'; }

step "1. A three-track file for polyemesis to dial"
mkdir -p data/recordings
# Three tracks at well-separated frequencies, so each one is identifiable in an
# output afterwards rather than merely assumed to be there.
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=640x360:rate=30" \
  -f lavfi -i "sine=frequency=300:sample_rate=48000" \
  -f lavfi -i "sine=frequency=1200:sample_rate=48000" \
  -f lavfi -i "sine=frequency=5000:sample_rate=48000" \
  -map 0:v -map 1:a -map 2:a -map 3:a \
  -c:v libx264 -preset ultrafast -g 60 -pix_fmt yuv420p -b:v 1200k \
  -c:a aac -b:a 128k -ac 2 -t 20 \
  -y data/recordings/loop.ts 2>/dev/null
if [ -s data/recordings/loop.ts ]; then
  ok "built a 3-track pull source"
else
  bad "could not build the pull source"; exit 1
fi

step "2. Server, configured to PULL rather than listen"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }

OUT=$(go run "$SCRIPTS/acceptance_pull_driver.go" "http://127.0.0.1:$PORT" 2>&1)
case "$OUT" in
  *SETUP_OK*)   ok "first-run setup" ;;
  *)            bad "setup failed: $OUT"; exit 1 ;;
esac
case "$OUT" in
  *PULL_MODE_OK*) ok "ingest switched to pull, dialling file://recordings/loop.ts" ;;
  *)              bad "could not switch to pull: $OUT"; exit 1 ;;
esac
case "$OUT" in
  *DESTS_OK*)   ok "two destinations with different track selections" ;;
  *)            bad "destination creation failed: $OUT"; exit 1 ;;
esac

step "3. The dialled source is probed"
probed=no
for _ in $(seq 1 40); do
  n=$(go run "$SCRIPTS/acceptance_pull_driver.go" "http://127.0.0.1:$PORT" tracks 2>/dev/null | tail -1)
  if [ "${n:-0}" -ge 3 ] 2>/dev/null; then probed=yes; break; fi
  sleep 1
done
if [ "$probed" = yes ]; then
  ok "pull ingest probed 3 audio tracks"
else
  bad "pull ingest never probed its tracks (got ${n:-none})"
fi

step "4. Each destination carries exactly its own mix"
sleep 18
go run "$SCRIPTS/acceptance_pull_driver.go" "http://127.0.0.1:$PORT" stopall >/dev/null 2>&1
sleep 8

A3=$(rms data/recordings/pullA.mkv 300 100)
A12=$(rms data/recordings/pullA.mkv 1200 200)
B12=$(rms data/recordings/pullB.mkv 1200 200)
B3=$(rms data/recordings/pullB.mkv 300 100)

if [ -n "$A3" ] && [ -n "$B12" ]; then
  printf "        destA (track 1) 300Hz %s   1200Hz %s\n" "$A3" "$A12"
  printf "        destB (track 2) 300Hz %s   1200Hz %s\n" "$B3" "$B12"
  louder_than "$A3" "-45"  && ok "destination A carries track 1" \
                           || bad "destination A is silent (300Hz $A3)"
  louder_than "$B12" "-45" && ok "destination B carries track 2" \
                           || bad "destination B is silent (1200Hz $B12)"
  # The separation, which is the point: pull must route per destination exactly
  # as a listened-for ingest does.
  if louder_than "$A3" "$(awk -v x="$A12" 'BEGIN{print x+15}')"; then
    ok "destination A did not receive track 2"
  else
    bad "tracks bled: destA has 1200Hz at $A12 against its own $A3"
  fi
  if louder_than "$B12" "$(awk -v x="$B3" 'BEGIN{print x+15}')"; then
    ok "destination B did not receive track 1"
  else
    bad "tracks bled: destB has 300Hz at $B3 against its own $B12"
  fi
else
  bad "no output was produced by the pull ingest"
fi

step "5. Confinement"
OUT=$(go run "$SCRIPTS/acceptance_pull_driver.go" "http://127.0.0.1:$PORT" escape 2>&1)
case "$OUT" in
  *ESCAPE_REFUSED*) ok "a file:// source outside the data directory is refused" ;;
  *)                bad "an escaping pull source was accepted: $OUT" ;;
esac

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
# The count matters as much as the tally: a section that exits early leaves
# assertions unasked, and "0 failed" out of half a suite is not a pass.
EXPECTED_CHECKS=10
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -eq 0 ]; then
  printf "  \033[32mPULL ACCEPTANCE PASSED\033[0m\n\n"
else
  printf "  \033[31mPULL ACCEPTANCE FAILED\033[0m\n\n"
fi
[ "$fail" -eq 0 ]
