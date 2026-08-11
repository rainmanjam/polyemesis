#!/usr/bin/env bash
# End-state acceptance test for hardware-encoder capability detection.
#
# The bug this proves fixed: `ffmpeg -encoders` reports what the BUILD was
# compiled with, not what the MACHINE can do. A stock Linux FFmpeg lists
# h264_nvenc, h264_qsv, h264_vaapi and h264_amf on a box with no GPU in it at
# all. Offering those in the rendition editor means the user saves one, goes
# live, and the encode dies with "Cannot load libcuda.so.1".
#
# Four things are asserted, and only the first is about this machine:
#
#   1. DETECTION IS COHERENT HERE. Every encoder the build registers was test-
#      encoded, and every one that cannot run carries FFmpeg's own reason —
#      "unavailable" with no explanation is the failure mode being fixed.
#
#   2. THE LIE IS CAUGHT. A shim FFmpeg that LISTS h264_nvenc and fails to
#      encode with it — the stock-Linux-on-no-GPU case, reproduced on a machine
#      that has no NVIDIA hardware to reproduce it with — is offered as
#      unusable, and a rendition saved on it is REFUSED with that reason rather
#      than left crash-looping. A libx264 rendition on the same shim still runs,
#      so the refusal is targeted and not blanket.
#
#   3. IT IS CHEAP. Startup is timed with the probes running and with them
#      stubbed out, and the difference is reported and bounded.
#
#   4. IT FAILS OPEN. Pointed at an FFmpeg whose detection commands all error,
#      the server still starts, still offers every encoder, and still runs a
#      rendition to a correct 720p file. Detection that could not run must never
#      be the thing that stops a stream — the SRT check learned that once
#      already.
#
# Usage:  ./scripts/acceptance-encoders.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-encoders}"
PORT=8100
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
note() { printf "  \033[2m%s\033[0m\n" "$1"; }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"
}
trap 'poly_teardown_trap $? cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
REAL_FFMPEG="$(command -v ffmpeg)"
[ -n "$REAL_FFMPEG" ] || { echo "ffmpeg is required"; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK/bin"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm

# --------------------------------------------------------------- the shims
#
# Every shim delegates real work to the real FFmpeg and lies only about
# detection, so a rendition started under one of them genuinely encodes. That
# is what makes "refused" distinguishable from "could not have worked anyway".
#
# The probe is recognised by its source: no other command polyemesis spawns
# contains testsrc2 at 320x240.

# LIAR: lists an encoder it cannot run. This is a stock Linux FFmpeg on a box
# with no NVIDIA card, staged on a machine that has never seen one.
cat > bin/ffmpeg-liar <<EOF
#!/usr/bin/env bash
REAL="$REAL_FFMPEG"
for a in "\$@"; do
  if [ "\$a" = "-encoders" ]; then
    "\$REAL" "\$@"
    # The exact row shape FFmpeg uses, so parseVideoEncoders tokenises it the
    # same way it tokenises the genuine ones.
    echo " V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)"
    exit 0
  fi
done
prev=""; enc=""
for a in "\$@"; do
  if [ "\$prev" = "-c:v" ]; then enc="\$a"; fi
  prev="\$a"
done
if [ "\$enc" = "h264_nvenc" ]; then
  echo "[h264_nvenc @ 0x55d1c0a4b900] Cannot load libcuda.so.1" >&2
  echo "Error initializing output stream 0:0 -- Error while opening encoder for output stream #0:0" >&2
  exit 1
fi
exec "\$REAL" "\$@"
EOF

# BLIND: every detection command fails. -encoders is refused and the probe
# cannot generate its test pattern, so nothing at all is measured. Real encodes
# are untouched, which is the whole point: the machine works, the measurement
# does not.
cat > bin/ffmpeg-blind <<EOF
#!/usr/bin/env bash
REAL="$REAL_FFMPEG"
for a in "\$@"; do
  case "\$a" in
    -encoders) echo "ffmpeg: -encoders is not available in this build" >&2; exit 1 ;;
    testsrc2=size=320x240*) echo "Unknown filter 'testsrc2'" >&2; exit 1 ;;
  esac
done
exec "\$REAL" "\$@"
EOF

# INSTANT: the control for the timing run. Identical to the real FFmpeg except
# that a probe returns immediately, which is what startup looked like before
# any probing existed.
cat > bin/ffmpeg-instant <<EOF
#!/usr/bin/env bash
REAL="$REAL_FFMPEG"
for a in "\$@"; do
  case "\$a" in
    testsrc2=size=320x240*) exit 0 ;;
  esac
done
exec "\$REAL" "\$@"
EOF

chmod +x bin/ffmpeg-liar bin/ffmpeg-blind bin/ffmpeg-instant
ok "built three FFmpeg shims that lie only about detection"

# ---------------------------------------------------------------- helpers

# start_server <dir> <ffmpeg-binary>  -> leaves the server running
start_server() {
  local dir="$1" ff="$2"
  mkdir -p "$dir"
  "$BIN" -addr ":$PORT" -data "$dir/data" -ffmpeg "$ff" -log warn > "$dir/server.log" 2>&1 &
  for _ in $(seq 1 60); do
    sleep 0.2
    if grep -q "web ui" "$dir/server.log" 2>/dev/null; then return 0; fi
  done
  return 1
}

stop_server() {
  pkill -f "polyemesis -addr :$PORT" 2>/dev/null
  for _ in $(seq 1 40); do
    pgrep -f "polyemesis -addr :$PORT" >/dev/null 2>&1 || break
    sleep 0.2
  done
  sleep 0.5
}

relay_port() {
  local pid; pid=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
  lsof -nP -iUDP -a -p "$pid" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}'
}

# run_driver <mode> <facts-file>
run_driver() {
  go run "$SCRIPTS/acceptance_encoders_driver.go" "$PORT" "$(relay_port)" "$1" "$2" 2>&1 | sed 's/^/  /'
}

# fact <file> <key>
fact() { grep "^$2=" "$1" 2>/dev/null | head -1 | cut -d= -f2-; }

# ============================================================ 1. this machine
step "1. What this machine reports, and whether it hangs together"

start_server real "$REAL_FFMPEG" && ok "server started against the real FFmpeg" \
                                 || bad "server did not start"
run_driver inspect "$WORK/real.env"
R="$WORK/real.env"
stop_server

if [ ! -s "$R" ]; then
  bad "driver produced no facts; cannot assess detection"
else
  [ "$(fact "$R" TESTED)" = "true" ] \
    && ok "the server actually test-encoded, rather than reading a build list" \
    || bad "tested=false: nothing was measured on a machine with a working FFmpeg"

  [ "$(fact "$R" libx264_WORKS)" = "true" ] \
    && ok "libx264 encodes here ($(fact "$R" libx264_MS)ms)" \
    || bad "libx264 does not encode here: $(fact "$R" libx264_REASON)"
  [ "$(fact "$R" libx264_MEASURED)" = "true" ] \
    && ok "libx264's verdict is a measurement, not an assumption" \
    || bad "libx264 was never measured"

  # The platform's own encoder is the one that must be found when it is there.
  # Asserted only where the build registers it: this suite has to be runnable
  # on the Linux boxes the bug actually bites, and a hard videotoolbox
  # requirement would make it a macOS-only test.
  if [ "$(fact "$R" h264_videotoolbox_AVAILABLE)" = "true" ]; then
    [ "$(fact "$R" h264_videotoolbox_WORKS)" = "true" ] \
      && ok "h264_videotoolbox encodes here ($(fact "$R" h264_videotoolbox_MS)ms)" \
      || bad "the build registers videotoolbox but it failed: $(fact "$R" h264_videotoolbox_REASON)"
    [ "$(fact "$R" DEFAULT)" = "h264_videotoolbox" ] \
      && ok "a new rendition defaults to the working hardware encoder" \
      || bad "default is $(fact "$R" DEFAULT), expected the working hardware encoder"
  else
    note "this build has no videotoolbox; skipping the Apple assertions"
  fi

  # The assertion that holds on every machine and is the actual fix: nothing is
  # ever withheld silently. "Unavailable" with no reason is what sent the user
  # live on an encoder that could not run.
  #
  # Counted, because `for e in $(fact "$R" ALL_ENCODERS)` runs zero times when
  # that key is absent -- a renamed fact, a driver that stopped emitting it --
  # and `fact` answers a missing key with an empty string. Zero iterations
  # leaves $unexplained empty, which is this check's PASSING state: it would
  # report that every unusable encoder explains itself, having examined none.
  n_enc=0
  unexplained=""
  for e in $(fact "$R" ALL_ENCODERS); do
    n_enc=$((n_enc + 1))
    if [ "$(fact "$R" "${e}_WORKS")" = "false" ] && [ -z "$(fact "$R" "${e}_REASON")" ]; then
      unexplained="$unexplained $e"
    fi
  done
  if [ "$n_enc" -lt 1 ]; then
    bad "the driver reported no encoder list at all; this assertion had nothing to examine"
  elif [ -z "$unexplained" ]; then
    ok "every unusable encoder says why it is unusable ($n_enc examined)"
  else
    bad "reported unusable with no reason:$unexplained"
  fi

  # This box is Apple Silicon with no discrete GPU. Every other vendor's
  # encoder must come back unusable AND explained.
  if [ "$(uname -s)" = "Darwin" ]; then
    for e in h264_nvenc h264_qsv h264_vaapi h264_amf; do
      w=$(fact "$R" "${e}_WORKS"); r=$(fact "$R" "${e}_REASON")
      if [ "$w" = "false" ] && [ -n "$r" ]; then
        ok "$e is refused with a reason: $r"
      else
        bad "$e reported works=$w reason='$r' on a machine with no such silicon"
      fi
    done
  fi
fi

# ====================================================== 2. the listed-but-dead
step "2. An encoder the build LISTS but the machine cannot run"

start_server liar "$WORK/bin/ffmpeg-liar" && ok "server started against the lying FFmpeg" \
                                          || bad "server did not start"
run_driver refuse "$WORK/liar.env"
L="$WORK/liar.env"
stop_server

if [ ! -s "$L" ]; then
  bad "driver produced no facts for the refusal case"
else
  # The old code's entire input. It still says yes — which is exactly why it
  # was never enough on its own.
  [ "$(fact "$L" h264_nvenc_AVAILABLE)" = "true" ] \
    && ok "the build list still claims h264_nvenc (this is the old code's answer)" \
    || bad "the shim's -encoders row was not picked up; the test proves nothing"

  [ "$(fact "$L" h264_nvenc_WORKS)" = "false" ] \
    && ok "the test encode overrules the build list" \
    || bad "h264_nvenc reported working despite failing its test encode"

  case "$(fact "$L" h264_nvenc_REASON)" in
    *"Cannot load libcuda.so.1"*)
      ok "FFmpeg's own words reach the editor: $(fact "$L" h264_nvenc_REASON)" ;;
    *) bad "reason was '$(fact "$L" h264_nvenc_REASON)', expected the libcuda message" ;;
  esac

  # Inference, not measurement: only h264_* is probed, and the HEVC sibling
  # opens the same device through the same driver.
  [ "$(fact "$L" hevc_nvenc_WORKS)" = "false" ] \
    && ok "hevc_nvenc is withheld too, on its sibling's evidence" \
    || bad "hevc_nvenc still offered after h264_nvenc failed on the same device"
  [ "$(fact "$L" hevc_nvenc_MEASURED)" = "false" ] \
    && ok "and the editor says that verdict was inferred, not measured" \
    || bad "hevc_nvenc claims to have been measured; only h264_* is probed"

  # Only nvenc is broken under this shim; whatever this machine really has is
  # still expected to pass. The list must lose the one that failed and keep the
  # ones that did not.
  case " $(fact "$L" HARDWARE) " in
    *" h264_nvenc "*) bad "h264_nvenc is still in the offered hardware list after failing" ;;
    *) ok "the offered hardware list drops h264_nvenc and keeps the rest ($(fact "$L" HARDWARE))" ;;
  esac

  # The user-visible half. A saved rendition on the dead encoder must be told
  # no, in words, once — not restarted forever.
  case "$(fact "$L" NVENC_REND_ERROR)" in
    *"Cannot load libcuda.so.1"*)
      ok "the nvenc rendition is refused, quoting the driver failure" ;;
    "") bad "the nvenc rendition reported no error at all" ;;
    *)  bad "the nvenc rendition failed with '$(fact "$L" NVENC_REND_ERROR)', which does not name the cause" ;;
  esac
  [ "$(fact "$L" NVENC_REND_RUNNING)" = "false" ] \
    && ok "and no encoder process was spawned for it" \
    || bad "an encoder process is running for a rendition that cannot encode"
  [ "$(fact "$L" NVENC_PROC_SAMPLES_MAX)" = "0" ] \
    && ok "it is refused once, not crash-looped (0 processes across the whole sample window)" \
    || bad "saw $(fact "$L" NVENC_PROC_SAMPLES_MAX) encoder processes: it is crash-looping"

  # Targeted, not blanket.
  [ "$(fact "$L" X264_REND_RUNNING)" = "true" ] \
    && ok "a libx264 rendition on the same FFmpeg runs normally" \
    || bad "the libx264 rendition was refused too: $(fact "$L" X264_REND_ERROR)"
fi

# ============================================================== 3. what it costs
step "3. What detection adds to startup"

# `date` has no portable sub-second format across macOS and GNU, and this needs
# milliseconds to say anything useful about a few-hundred-millisecond probe.
now_ms() { perl -MTime::HiRes=time -e 'printf("%d\n", time()*1000)'; }

# Median of five, because the first launch of a 14MB binary pays a cold page
# cache that is worth more than the probe is.
time_startup() { # time_startup <dir-prefix> <ffmpeg> -> median ms
  local prefix="$1" ff="$2" i t0 t1
  local samples=()
  for i in 1 2 3 4 5; do
    rm -rf "$prefix$i"
    t0=$(now_ms)
    start_server "$prefix$i" "$ff" || { echo "-1"; return; }
    t1=$(now_ms)
    stop_server
    samples+=($((t1 - t0)))
  done
  printf '%s\n' "${samples[@]}" | sort -n | sed -n '3p'
}

BEFORE=$(time_startup timing-before "$WORK/bin/ffmpeg-instant")
AFTER=$(time_startup timing-after "$REAL_FFMPEG")
DELTA=$((AFTER - BEFORE))

note "startup with probes stubbed out : ${BEFORE}ms  (median of 5)"
note "startup with probes running     : ${AFTER}ms  (median of 5)"
note "difference                      : ${DELTA}ms"
note "slowest single probe, as the server measured it: $(fact "$R" MAX_PROBE_MS)ms"

# The probes run concurrently, so the cost is one probe's latency and not the
# sum. A second is the ceiling because it is the point at which a user notices
# the server took a moment to come up; anything above it means the candidate
# list or the concurrency needs revisiting rather than the ceiling raising.
if [ "$DELTA" -lt 1000 ]; then
  ok "detection adds ${DELTA}ms to startup, under the 1000ms ceiling"
else
  bad "detection adds ${DELTA}ms to startup, over the 1000ms ceiling"
fi
# Cross-check against the product's own per-probe measurement. They are
# independent measurements of the same thing and should not disagree wildly.
if [ -n "$(fact "$R" MAX_PROBE_MS)" ] && [ "$(fact "$R" MAX_PROBE_MS)" -lt 1000 ]; then
  ok "the slowest probe the server ran took $(fact "$R" MAX_PROBE_MS)ms"
else
  bad "the slowest probe took $(fact "$R" MAX_PROBE_MS)ms"
fi

# ======================================================= 4. detection fails open
step "4. Detection forced to fail entirely — the safe default"

start_server blind "$WORK/bin/ffmpeg-blind" && ok "server started even though every detection command errored" \
                                            || bad "server did not start when detection failed"
run_driver fallback "$WORK/blind.env"
B="$WORK/blind.env"
stop_server

if [ ! -s "$B" ]; then
  bad "driver produced no facts for the fail-open case"
else
  [ "$(fact "$B" PROBED)" = "false" ] \
    && ok "the encoder list is honestly reported as unreadable" \
    || bad "probed=true from an FFmpeg that refuses -encoders"

  # Nothing was demonstrated, so nothing may be withheld. Reporting the probe's
  # own failure as the encoders' failure would take away every encoder on the
  # box, software included, and refuse renditions that encode perfectly well.
  #
  # Same zero-iteration hazard as the ALL_ENCODERS loop in step 1, and worse
  # here: an empty list means the driver offered no encoders at all, which is
  # precisely the outcome this check exists to refuse -- and it would have been
  # reported as "every encoder is still offered".
  n_enc=0
  withheld=""
  for e in $(fact "$B" ALL_ENCODERS); do
    n_enc=$((n_enc + 1))
    [ "$(fact "$B" "${e}_WORKS")" = "false" ] && withheld="$withheld $e"
  done
  if [ "$n_enc" -lt 1 ]; then
    bad "no encoder was offered at all; that is the withholding this check refuses, not the absence of it"
  elif [ -z "$withheld" ]; then
    ok "every encoder is still offered when nothing could be measured ($n_enc offered)"
  else
    bad "withheld on a measurement that never happened:$withheld"
  fi

  [ "$(fact "$B" TESTED)" = "false" ] \
    && ok "and the UI is told those verdicts are unmeasured" \
    || bad "tested=true when no probe succeeded"

  [ "$(fact "$B" DEFAULT)" = "libx264" ] \
    && ok "the default falls back to software" \
    || bad "default is $(fact "$B" DEFAULT), expected libx264 with nothing measured"

  [ "$(fact "$B" X264_REND_RUNNING)" = "true" ] \
    && ok "a rendition still starts" \
    || bad "the rendition was refused: $(fact "$B" X264_REND_ERROR)"

  out="blind/data/recordings/fallback.mkv"
  if [ -s "$out" ]; then
    dim=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$out")
    [ "$dim" = "1280,720" ] \
      && ok "and it produced a real 720p encode ($dim)" \
      || bad "rendition output is $dim, expected 1280,720"
  else
    bad "the rendition produced no output file"
  fi
fi

# ------------------------------------------------------------------ summary
step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
printf "  \033[32mENCODER ACCEPTANCE PASSED\033[0m\n\n"
