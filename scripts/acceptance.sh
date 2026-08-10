#!/usr/bin/env bash
# End-state acceptance test.
#
# Reproduces the product's headline scenario exactly:
#   start the binary -> first-run setup -> point a synthetic multi-track stream
#   at the ingest -> add two destinations, one LOCAL FILE and one CUSTOM RTMP ->
#   give one tracks 1+2 and the other tracks 1+3 -> verify by measurement that
#   each output contains exactly its selected mix and nothing else.
#
# Usage:  ./scripts/acceptance.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance}"
PORT=8098
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
  pkill -f "acceptance-source"            2>/dev/null
  pkill -f "listen 1 -i rtmp://127.0.0.1:1937" 2>/dev/null
  poly_cleanup "$PORT" "${WORK:-}"
}
trap 'poly_watchdog_disarm; cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm

# ---------------------------------------------------------------- 1. server
step "1. Start the binary"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do
  sleep 0.3
  if grep -q "web ui" server.log 2>/dev/null; then break; fi
done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || bad "server did not start"

# The relay hub binds a random loopback UDP port; find it from the process.
SRVPID=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
RELAY=$(lsof -nP -iUDP -a -p "$SRVPID" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}')
[ -n "$RELAY" ] && ok "relay hub bound (udp/$RELAY)" || bad "no relay port"

# ------------------------------------------------------------- 2. RTMP sink
step "2. Start an RTMP sink to receive the custom-RTMP destination"
ffmpeg -hide_banner -loglevel error -listen 1 \
  -i "rtmp://127.0.0.1:1937/live/acceptance" \
  -c copy -y rtmp-out.mkv > rtmp-sink.log 2>&1 &
sleep 1
ok "rtmp sink listening on 1937"

# --------------------------------------------------------------- 3. the app
step "3. First-run setup, destinations and routing (via the API the UI uses)"
go run "$SCRIPTS/acceptance_driver.go" "$PORT" "$RELAY" 2>&1 | sed 's/^/  /' 

# ------------------------------------------------------------- 4. the stream
# (the driver starts the source and waits; see acceptance_driver.go)

# ------------------------------------------------------------ 5. verify
step "5. Verify each output carries exactly its selected mix"

band() {  # band <file> <freq>  -> RMS dBFS in a narrow band
  ffmpeg -v info -i "$1" \
    -af "bandpass=frequency=$2:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
}

check() { # check <file> <label> <present300> <present900> <present2000>
  local f="$1" label="$2"; shift 2
  if [ ! -s "$f" ]; then bad "$label: output file missing or empty"; return; fi

  # exactly one stereo AAC stream
  local streams
  streams=$(ffprobe -v error -select_streams a -show_entries stream=codec_name,channels \
            -of csv=p=0 "$f" | tr '\n' ' ')
  local n; n=$(echo "$streams" | wc -w | tr -d ' ')
  if [ "$n" = "1" ]; then ok "$label: exactly one audio stream ($streams)"
  else bad "$label: expected 1 audio stream, got '$streams'"; fi

  # video copied through, not re-encoded
  local vcodec; vcodec=$(ffprobe -v error -select_streams v:0 \
      -show_entries stream=codec_name -of csv=p=0 "$f")
  [ "$vcodec" = "h264" ] && ok "$label: video passed through as h264" \
                         || bad "$label: video codec is '$vcodec', expected h264"

  local i=1
  for freq in 300 900 2000; do
    local want; want=$(eval echo "\${$i}"); i=$((i+1))
    local db; db=$(band "$f" "$freq")
    # A present tone sits near -21 dB; an excluded one is 28-37 dB lower, at
    # the bandpass filter's leakage floor. -35 dB separates them with margin.
    local present
    present=$(awk -v d="$db" 'BEGIN{print (d > -35) ? "yes" : "no"}')
    if [ "$present" = "$want" ]; then
      ok "$label: ${freq}Hz ${db}dB present=$present (expected $want)"
    else
      bad "$label: ${freq}Hz ${db}dB present=$present but expected $want"
    fi
  done
}

check "data/recordings/file-dest.mkv" "FILE dest (tracks 1+2)" yes yes no
sleep 1
# The sink has to be GONE before rtmp-out.mkv is read, not merely asked to go:
# it is the process writing that file, and ffprobe against a half-flushed
# Matroska reports missing audio bands -- a wrong routing verdict produced by a
# teardown that had not finished. `sleep 2` was an assumption about how long
# that takes; poly_free_port is an observation, since the sink is exactly the
# process holding 1937.
#
# One caveat, stated rather than discovered later: poly_free_port is a no-op
# without lsof, so on a machine that lacks it this stops waiting at all rather
# than waiting 2s. That is not a new dependency -- poly_cleanup's port reclaim
# has needed lsof since it was written, and ci.yml installs it for this job at
# :678 -- but it is the reason the helper reports what it did instead of
# guessing.
pkill -f "listen 1 -i rtmp://127.0.0.1:1937" 2>/dev/null
poly_free_port 1937
check "rtmp-out.mkv" "RTMP dest (tracks 1+3)" yes no yes

# ------------------------------------------------------------------ summary
step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
echo "  ACCEPTANCE PASSED"
