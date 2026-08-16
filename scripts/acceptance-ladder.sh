#!/usr/bin/env bash
# End-state acceptance test for a rendition LADDER.
#
# scripts/acceptance-renditions.sh proves a rendition works, and its central
# assertion is "exactly ONE encoder process served both destinations for the
# whole run". One is the only number it ever counts, because every test in it
# uses ONE rendition. The real-world case -- several destinations each wanting a
# DIFFERENT resolution out of one ingest -- has never been measured, and it is
# the case docs/ENCODING.md says costs real money:
#
#   "Cost scales with distinct renditions, not with destinations... A rendition
#    is shared and ref-counted: one encode feeds every destination that selected
#    it. Five destinations on one 1080p tier is one encode, not five."
#
# So this counts N rather than 1, which is the harder assertion, and it counts
# it going both up and down:
#
#   1920x1080 3-tone ingest
#     ├─ tier "1080p"  scale=1920:1080  ← dest A   tracks 1+2
#     ├─ tier "720p"   scale=1280:720   ← dest B   tracks 1+3
#     │                                 ← dest D   track  1     (joins later)
#     └─ tier "480p"   scale=854:480    ← dest C   tracks 2+3
#
# and asserts, by measurement:
#   - three distinct tiers produce exactly THREE encoder processes, one per
#     tier, for the whole run -- not one shared by everybody, and not four;
#   - each destination receives ITS OWN resolution, probed off the delivered
#     media rather than read back out of the configuration that asked for it;
#   - a FOURTH destination selecting an EXISTING tier is still three encoders,
#     and the encoder it joined is the same PROCESS, not a restarted one;
#   - removing one of two subscribers leaves that tier's encode alone, and
#     removing the LAST subscriber stops it while the other two tiers keep the
#     process they already had;
#   - every destination still gets its OWN audio mix, including the two sharing
#     one video encode, by per-band RMS energy;
#   - what it all cost, in CPU-seconds per wall-second per tier, measured and
#     printed so a human can compare runs.
#
# Usage:  ./scripts/acceptance-ladder.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-ladder}"
PORT=8101
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

# THE SOURCE HAS TO BE BIG ENOUGH TO STEP DOWN FROM. The other suites use small
# fixtures deliberately, for speed; a ladder cannot, because 720p and 480p are
# only real reductions if the ingest is above them.
#
# POLY_MS_WIDTH / POLY_MS_HEIGHT are acceptance-multistream.sh's existing knobs
# for exactly this question, with exactly these defaults, and they are reused
# rather than duplicated under a new name: they mean "how big is the synthetic
# broadcast an acceptance suite publishes", which is the same question here. One
# knob that shrinks every large-source suite on a small machine is worth more
# than two that each shrink half of them.
LADDER_WIDTH="${POLY_MS_WIDTH:-1920}"
LADDER_HEIGHT="${POLY_MS_HEIGHT:-1080}"
# 30 rather than multistream's 60, and NOT parameterised. Frame-rate step-down
# is already proven by acceptance-renditions.sh; here every tier runs at the
# source rate so that RESOLUTION is the only variable, and a tier that came back
# at the wrong size cannot be confused with one that came back at the wrong
# rate. It also halves the encoding work, which matters when three encodes run
# at once.
LADDER_FPS=30

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"
}
trap 'poly_teardown_trap $? cleanup' EXIT

[[ -x "$BIN" ]] || { echo "build first: make build"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK" || exit 1
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

# The ingest relay hub binds a random loopback UDP port. Taken now, before any
# rendition exists, so it cannot be confused with a rendition's own hub.
SRVPID=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
RELAY=$(lsof -nP -iUDP -a -p "$SRVPID" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}')
[[ -n "$RELAY" ]] && ok "ingest relay hub bound (udp/$RELAY)" || ok "relay not bound yet -- the driver discovers it from the server after creating the source"

# --------------------------------------------------------------- 2. the app
step "2. Build the ladder and drive it (via the API the UI uses)"
FACTS="$WORK/facts.env"
# RUN FROM $ROOT, IN A SUBSHELL, and that is required rather than tidy. The
# driver imports scripts/internal/driverlib, and `go run` resolves a module
# import against the CURRENT DIRECTORY's go.mod rather than against the source
# file's location -- this suite has already cd'd into $WORK, which is under
# /tmp and inside no module at all. driverlib's package comment records the
# same trap for the two suites that hit it first. The facts path is absolute
# for exactly this reason, and the driver writes nothing else to its cwd.
( cd "$ROOT" && go run "$SCRIPTS/acceptance_ladder_driver.go" "$PORT" "$RELAY" "$FACTS" \
  "$LADDER_WIDTH" "$LADDER_HEIGHT" "$LADDER_FPS" 2>&1 ) | sed 's/^/  /'

[[ -s "$FACTS" ]] || { bad "driver wrote no facts"; step "Summary"; printf "  %d passed, %d failed\n\n" "$pass" "$fail"; exit 1; }
# shellcheck disable=SC1090
source "$FACTS"

if [[ -n "${DRIVER_FAILED:-}" ]]; then bad "driver aborted: $DRIVER_FAILED"; fi

# ------------------------------------------- 3. distinct tiers, distinct encodes
step "3. Verify three distinct tiers produced three distinct encodes"

# The claim acceptance-renditions.sh cannot make. It counts ONE encoder; a
# ladder has to count N, and N is what separates "renditions are shared" from
# "renditions are shared and there is more than one of them".
#
# Every number below is read out of the live process table by the driver, never
# out of /status. The API could truthfully report three renditions while the
# engine had spawned one encode and handed it to everybody, or four because a
# ref count leaked -- and those are the two failures this whole file exists for.

[[ "${PROCS_BEFORE_SELECT:-x}" = "0" ]] \
  && ok "three renditions nothing has selected burn no CPU (0 encoder processes)" \
  || bad "three unselected renditions had ${PROCS_BEFORE_SELECT:-?} encoder processes, expected 0"

# Locating aid rather than the claim: if the store dropped a dimension, the
# encoder comes up at the wrong size and the pixel checks below fail somewhere
# nobody could trace back to here.
if [[ "${TIER_1080_W_STORED:-}" = "1920" && "${TIER_1080_H_STORED:-}" = "1080" ]] \
&& [[ "${TIER_720_W_STORED:-}"  = "1280" && "${TIER_720_H_STORED:-}"  = "720" ]] \
&& [[ "${TIER_480_W_STORED:-}"  = "854" && "${TIER_480_H_STORED:-}"  = "480" ]]; then
  ok "all three tier sizes round-tripped through the store"
else
  bad "the store returned 1080p=${TIER_1080_W_STORED:-}x${TIER_1080_H_STORED:-} 720p=${TIER_720_W_STORED:-}x${TIER_720_H_STORED:-} 480p=${TIER_480_W_STORED:-}x${TIER_480_H_STORED:-}; the encoder checks below cannot mean anything until this does"
fi

# THE ASSERTION THE FEATURE COSTS MONEY FOR. Three destinations, three tiers,
# three encodes -- for the WHOLE window, not at one lucky instant.
if [[ "${PROCS_MIN_A0:-0}" = "3" && "${PROCS_MAX_A0:-0}" = "3" ]]; then
  ok "exactly THREE encoder processes served the three tiers for the whole window"
else
  bad "encoder processes ranged ${PROCS_MIN_A0:-?}..${PROCS_MAX_A0:-?} across the three tiers, expected exactly 3"
fi

# And one EACH, which the total alone cannot say: three encoders all at 720p
# would total three and would mean the ladder collapsed onto one rung.
for t in 1080 720 480; do
  # Indirect expansion rather than eval: the fact names are built from the tier
  # label, and eval would happily execute anything that ended up in one.
  mnv="N_${t}_MIN_A0"; mxv="N_${t}_MAX_A0"
  mn="${!mnv:-0}"; mx="${!mxv:-0}"
  if [[ "$mn" = "1" && "$mx" = "1" ]]; then
    ok "the ${t}p tier ran exactly one encoder of its own for the whole window"
  else
    bad "the ${t}p tier ran ${mn}..${mx} encoders over the window, expected exactly 1"
  fi
done

# Each rung publishes to its OWN hub. That is the mechanism by which one encode
# can feed several destinations, so three rungs must show three distinct ports,
# and none of them the ingest's -- a rung sharing the ingest's hub would be
# republishing the source rather than its own encode.
HUBS=$(printf '%s\n%s\n%s\n' "${RELAY_1080:-0}" "${RELAY_720:-0}" "${RELAY_480:-0}" | sort -u | wc -l | tr -d ' ')
if [[ "$HUBS" = "3" ]] \
&& [[ "${RELAY_1080:-0}" != "${INGEST_RELAY_PORT:-0}" ]] \
&& [[ "${RELAY_720:-0}"  != "${INGEST_RELAY_PORT:-0}" ]] \
&& [[ "${RELAY_480:-0}"  != "${INGEST_RELAY_PORT:-0}" ]] \
&& [[ "${RELAY_1080:-0}" != "0" ]]; then
  ok "the three tiers publish to three distinct hubs (udp/${RELAY_1080}, ${RELAY_720}, ${RELAY_480}), none the ingest's (${INGEST_RELAY_PORT})"
else
  bad "tier hubs were ${RELAY_1080:-?}/${RELAY_720:-?}/${RELAY_480:-?} against the ingest's ${INGEST_RELAY_PORT:-?}: expected three distinct ports, none of them the ingest's"
fi

# --------------------------------------- 4. each destination's OWN resolution
step "4. Verify each destination received ITS OWN resolution"

vprop() { # vprop <file> <entry>
  ffprobe -v error -select_streams v:0 -show_entries "stream=$2" -of csv=p=0 "$1"
}

# MEASURED_<label> is the delivered frame size, set by check_video and compared
# against the OTHER destinations further down. Probing the media rather than
# reading /status back is the point of the whole step: a ladder that stored
# three sizes and encoded one would look perfect from the API.
MEASURED_1080=""; MEASURED_720=""; MEASURED_480=""; MEASURED_720B=""

check_video() { # check_video <file> <label> <var> <wantW> <wantH>
  local f="$1" label="$2" var="$3" ww="$4" wh="$5"
  if [[ ! -s "$f" ]]; then bad "$label: output file missing or empty"; return 1; fi

  local w h; w=$(vprop "$f" width); h=$(vprop "$f" height)
  eval "$var=\"\${w}x\${h}\""

  # Either orientation counts as the requested frame size: nothing in the
  # pipeline rotates, but pinning only one order would fail for the wrong
  # reason on a portrait source.
  if [[ ( "$w" == "$ww" && "$h" == "$wh" ) || ( "$w" == "$wh" && "$h" == "$ww" ) ]]; then
    ok "$label: delivered video is ${w}x${h} (asked for ${ww}x${wh})"
  else
    bad "$label: delivered video is ${w}x${h}, asked for ${ww}x${wh}"
  fi
}

OUT=data/recordings
check_video "$OUT/ladder-1080.mkv" "dest A on the 1080p tier" MEASURED_1080 1920 1080
check_video "$OUT/ladder-720.mkv"  "dest B on the 720p tier"  MEASURED_720  1280 720
check_video "$OUT/ladder-480.mkv"  "dest C on the 480p tier"  MEASURED_480  854  480
check_video "$OUT/ladder-720b.mkv" "dest D on the 720p tier"  MEASURED_720B 1280 720

# The claim stated as a RELATION rather than three absolutes, because the
# failure this catches is a ladder that quietly serves one encode to everyone.
# Three destinations that each matched their own target ALREADY prove it -- but
# only if the targets differed, and stating that separately is what makes the
# suite fail loudly rather than subtly if someone ever makes two tiers the same
# size while tidying.
DISTINCT=$(printf '%s\n%s\n%s\n' "$MEASURED_1080" "$MEASURED_720" "$MEASURED_480" | sort -u | wc -l | tr -d ' ')
if [[ "$DISTINCT" = "3" ]]; then
  ok "the three tiers delivered three DIFFERENT frame sizes ($MEASURED_1080, $MEASURED_720, $MEASURED_480)"
else
  bad "the three tiers delivered $DISTINCT distinct frame sizes ($MEASURED_1080, $MEASURED_720, $MEASURED_480); one encode was served to more than one tier"
fi

# The other direction, and it is not redundant: two destinations on the SAME
# rung must receive the SAME picture. If they differed, the engine started them
# a private encode each and the ref count means nothing.
if [[ -n "$MEASURED_720" && "$MEASURED_720" = "$MEASURED_720B" ]]; then
  ok "both destinations on the 720p tier received the same frame size ($MEASURED_720)"
else
  bad "the two destinations on the 720p tier received '$MEASURED_720' and '$MEASURED_720B'; one shared encode cannot produce two sizes"
fi

# ---------------------------------------------- 5. ref counting, upwards
step "5. Verify ref counting UP: a fourth destination on an existing tier"

# "Five destinations on one 1080p tier is one encode, not five", reduced to the
# smallest case that can distinguish it.
if [[ "${PROCS_MIN_A1:-0}" = "3" && "${PROCS_MAX_A1:-0}" = "3" ]]; then
  ok "a FOURTH destination on an existing tier left the count at three encoders"
else
  bad "with four destinations on three tiers the encoder count ranged ${PROCS_MIN_A1:-?}..${PROCS_MAX_A1:-?}, expected exactly 3"
fi

# SAME PROCESS, not merely "a process". A count cannot tell a reused encode from
# a restarted one, and they are very different: a restart drops frames on every
# destination that was already on that rung. This is the check that says the new
# subscriber ATTACHED.
if [[ -n "${PID_720_A0:-}" && "${PID_720_A0:-}" = "${PID_720_A1:-}" ]]; then
  ok "the 720p encoder the fourth destination joined is the same process (pid ${PID_720_A1}), not a restart"
else
  bad "the 720p encoder was pid '${PID_720_A0:-}' before the fourth destination and '${PID_720_A1:-}' after; joining a tier restarted its encode"
fi

if [[ "${CONSUMERS_720_A1:-0}" = "2" && "${CONSUMERS_1080_A1:-0}" = "1" && "${CONSUMERS_480_A1:-0}" = "1" ]]; then
  ok "the tiers report 2 / 1 / 1 consumers (720p, 1080p, 480p)"
else
  bad "the tiers report ${CONSUMERS_720_A1:-?} / ${CONSUMERS_1080_A1:-?} / ${CONSUMERS_480_A1:-?} consumers (720p, 1080p, 480p), expected 2 / 1 / 1"
fi

# ---------------------------------------------- 6. ref counting, downwards
step "6. Verify ref counting DOWN: removals release exactly one tier"

# The direction most likely to leak an orphaned FFmpeg, and the one nothing has
# measured. Two removals, and they must have DIFFERENT outcomes: the first takes
# a subscriber off a tier that still has one, the second takes the last.

[[ "${PROCS_AFTER_DROP_ONE:-0}" = "3" ]] \
  && ok "removing one of two subscribers left all three encoders running" \
  || bad "after removing one of the 720p tier's two destinations there were ${PROCS_AFTER_DROP_ONE:-?} encoders, expected 3"

if [[ "${N_720_AFTER_DROP_ONE:-0}" = "1" && "${PID_720_AFTER_DROP_ONE:-}" = "${PID_720_A1:-x}" ]]; then
  ok "the 720p encode another destination still uses was not disturbed (still pid ${PID_720_AFTER_DROP_ONE})"
else
  bad "after the removal the 720p tier had ${N_720_AFTER_DROP_ONE:-?} encoder(s) at pid '${PID_720_AFTER_DROP_ONE:-}', against '${PID_720_A1:-}' before; a tier another destination still uses was killed or restarted"
fi

[[ "${CONSUMERS_720_AFTER_DROP_ONE:-0}" = "1" ]] \
  && ok "the 720p tier reports 1 consumer after the removal" \
  || bad "the 720p tier reports ${CONSUMERS_720_AFTER_DROP_ONE:-?} consumers after removing one of two, expected 1"

# The other half, and the one an orphan hides in: the LAST subscriber leaving
# must actually stop the encode.
if [[ "${N_720_AFTER_DROP_LAST:-1}" = "0" && "${PROCS_AFTER_DROP_LAST:-0}" = "2" ]]; then
  ok "removing the LAST subscriber stopped that tier's encode and only that one (2 encoders left)"
else
  bad "after the last 720p subscriber went there were ${N_720_AFTER_DROP_LAST:-?} 720p encoders and ${PROCS_AFTER_DROP_LAST:-?} in total, expected 0 and 2"
fi

# The tiers nobody touched must be the SAME processes. A reconcile that restarts
# the world on every removal would pass every count above and would still be a
# bug: it interrupts destinations that had nothing to do with the change.
if [[ -n "${PID_1080_A1:-}" && "${PID_1080_AFTER_DROP_LAST:-}" = "${PID_1080_A1:-}" ]] \
&& [[ -n "${PID_480_A1:-}" && "${PID_480_AFTER_DROP_LAST:-}"  = "${PID_480_A1:-}" ]]; then
  ok "the 1080p and 480p encodes were untouched throughout (pids ${PID_1080_A1} and ${PID_480_A1})"
else
  bad "the untouched tiers changed process: 1080p '${PID_1080_A1:-}'->'${PID_1080_AFTER_DROP_LAST:-}', 480p '${PID_480_A1:-}'->'${PID_480_AFTER_DROP_LAST:-}'; removing a destination restarted tiers it had nothing to do with"
fi

if [[ "${CONSUMERS_720_AFTER_DROP_LAST:-1}" = "0" && "${RENDITION_720_RUNNING_AFTER_DROP_LAST:-yes}" = "no" ]]; then
  ok "the idle 720p tier reports 0 consumers and no process"
else
  bad "after the last removal the 720p tier reports consumers=${CONSUMERS_720_AFTER_DROP_LAST:-?} running=${RENDITION_720_RUNNING_AFTER_DROP_LAST:-?}"
fi

[[ "${PROCS_AFTER_ALL_STOPPED:-1}" = "0" ]] \
  && ok "no encoder survived the last destination stopping" \
  || bad "${PROCS_AFTER_ALL_STOPPED:-?} encoder process(es) still running after every destination stopped, expected 0"

# ----------------------------------------------- 7. audio, per destination
step "7. Verify audio stayed per-destination across the ladder"

# A rendition re-encodes VIDEO ONLY -- `-map 0:a -c:a copy` -- so every ingest
# track must still arrive at every destination, and each destination's own mix
# must still be its own. The technique is acceptance-audio.sh's: a narrow
# bandpass and the RMS energy left in it.
#
# The pair that matters is B and D. They copy the SAME video bitstream out of
# the SAME encoder process, and they must still come out with different audio.

band() {  # band <file> <freq>  -> RMS dBFS in a narrow band
  # -v info, NOT -v error: astats writes at INFO level, so -v error suppresses
  # the one line this parses and the function returns nothing -- which reads as
  # "the tone is absent" and is wrong.
  ffmpeg -v info -i "$1" \
    -af "bandpass=frequency=$2:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
}

check_audio() { # check_audio <file> <label> <present300> <present900> <present2000>
  local f="$1" label="$2"; shift 2
  if [[ ! -s "$f" ]]; then
    # Four named failures rather than one, so the count of checks that RAN is
    # the same whether the file was there or not. A missing file that skipped
    # its three tone checks would quietly shrink the suite.
    bad "$label: output file missing or empty"
    bad "$label: 300Hz not measured (no file)"
    bad "$label: 900Hz not measured (no file)"
    bad "$label: 2000Hz not measured (no file)"
    return
  fi

  # Exactly one stereo AAC stream: the rendition copied every track through and
  # the DESTINATION mixed them down. If a rendition had touched audio, this is
  # where it would show up.
  local streams n
  streams=$(ffprobe -v error -select_streams a -show_entries stream=codec_name,channels \
            -of csv=p=0 "$f" | tr '\n' ' ')
  n=$(echo "$streams" | wc -w | tr -d ' ')
  if [[ "$n" = "1" ]]; then ok "$label: exactly one audio stream ($streams)"
  else bad "$label: expected 1 audio stream, got '$streams'"; fi

  local i=1
  for freq in 300 900 2000; do
    local want db present
    want=$(eval echo "\${$i}"); i=$((i+1))
    db=$(band "$f" "$freq")
    # A present tone sits near -21 dB; an excluded one is 28-37 dB lower, at the
    # bandpass filter's leakage floor. -35 dB separates them with margin.
    present=$(awk -v d="$db" 'BEGIN{print (d > -35) ? "yes" : "no"}')
    if [[ "$present" = "$want" ]]; then
      ok "$label: ${freq}Hz ${db}dB present=$present (expected $want)"
    else
      bad "$label: ${freq}Hz ${db}dB present=$present but expected $want"
    fi
  done
}

check_audio "$OUT/ladder-1080.mkv" "dest A 1080p (tracks 1+2)" yes yes no
check_audio "$OUT/ladder-720.mkv"  "dest B 720p  (tracks 1+3)" yes no  yes
check_audio "$OUT/ladder-480.mkv"  "dest C 480p  (tracks 2+3)" no  yes yes
check_audio "$OUT/ladder-720b.mkv" "dest D 720p  (track 1)"    yes no  no

# ------------------------------------------------------------ 8. what it cost
step "8. What the ladder cost, measured"

# MEASURED AND PRINTED, not asserted against a number pulled out of the air.
# A CPU ceiling that would hold on a six-core laptop and on a two-core CI runner
# and on whatever anyone runs this on next does not exist, and inventing one
# would produce either a check that cannot fail or a flake. A measured figure a
# human can compare across runs is worth more than a fabricated ceiling.
printf "  tier   encoders   CPU (cores)\n"
printf "  1080p  %-10s %s\n" "${N_1080_MAX_A0:-?}" "${RATE_1080_A0:-?}"
printf "  720p   %-10s %s\n" "${N_720_MAX_A0:-?}"  "${RATE_720_A0:-?}"
printf "  480p   %-10s %s\n" "${N_480_MAX_A0:-?}"  "${RATE_480_A0:-?}"
printf "  ----------------------------\n"
printf "  3 destinations, 3 tiers:  %s cores over %ss\n" "${RATE_TOTAL_A0:-?}" "${WINDOW_SECS_A0:-?}"
printf "  4 destinations, 3 tiers:  %s cores over %ss\n" "${RATE_TOTAL_A1:-?}" "${WINDOW_SECS_A1:-?}"

# The one bound that IS defensible, and its derivation is the whole reason it is
# here. Adding a destination to a tier that already exists must not cost another
# encode -- that is the sentence in ENCODING.md, and an encode's price is not a
# constant, it is whatever the cheapest tier measured on THIS machine in THIS
# run. So the yardstick is measured alongside the thing it judges: if ref
# counting had failed and started a second 720p encoder, the total would have
# risen by a 720p encoder's worth, which is at least the cheapest tier's. The
# assertion is that the rise is below that.
#
# It is deliberately a corroboration and not the primary evidence: the process
# count above is unambiguous and this is a check on the COST, which is the thing
# the documentation actually promises.
# A ZERO YARDSTICK IS A BROKEN MEASUREMENT, not an expensive ladder, and it is
# reported that way for the reason this repository keeps rediscovering: a check
# that says "the tier was not shared" when the truth is "ps told us nothing"
# sends someone to read the engine instead of the parser. Mutating
# parseCPUTime to return 0 produces exactly this, and it must not be
# indistinguishable from a real cost regression.
if [[ -z "${RATE_TOTAL_A0:-}" || -z "${RATE_TOTAL_A1:-}" ]] \
|| [[ -z "${RATE_CHEAPEST_TIER_A0:-}" || "${RATE_CHEAPEST_TIER_A0}" = "0.00" ]]; then
  bad "the CPU measurement produced no usable reading (A0='${RATE_TOTAL_A0:-}' A1='${RATE_TOTAL_A1:-}' cheapest='${RATE_CHEAPEST_TIER_A0:-}'); the measurement is broken, not the cost"
else
  VERDICT=$(awk -v a="$RATE_TOTAL_A0" -v b="$RATE_TOTAL_A1" -v c="$RATE_CHEAPEST_TIER_A0" \
    'BEGIN{ printf "%s %.2f", ((b - a) < c ? "ok" : "bad"), b - a }')
  DELTA=${VERDICT#* }
  case "$VERDICT" in
    ok*) ok "the fourth destination added ${DELTA} cores, below the ${RATE_CHEAPEST_TIER_A0} a fourth encode would have cost" ;;
    *)   bad "the fourth destination added ${DELTA} cores against a cheapest tier of ${RATE_CHEAPEST_TIER_A0}; that is the price of another encode, so the tier was not shared" ;;
  esac
fi

# Each tier burned MEASURABLE CPU. Not a performance bound -- a guard on the
# measurement itself. An encoder counted out of the process table that consumed
# no CPU over eighteen seconds is not encoding, and every count above it would
# be counting a corpse.
for t in 1080 720 480; do
  rv="RATE_${t}_A0"; r="${!rv:-0}"
  if awk -v r="$r" 'BEGIN{exit !(r > 0)}'; then
    ok "the ${t}p encoder consumed measurable CPU (${r} cores), so it was really encoding"
  else
    bad "the ${t}p encoder consumed ${r} cores over ${WINDOW_SECS_A0:-?}s; a process using no CPU is not an encode, and the counts above counted it as one"
  fi
done

# ------------------------------------------------------------------ summary
step "Summary"

# COUNT WHAT RAN, NOT WHAT WAS CONFIGURED -- applied to this file itself.
#
# A `grep` that matches nothing inside a `||`-guarded pipeline is the shell's
# version of `go test -run <mistyped>`: it exits 0, reports nothing and looks
# like a pass. The only defence is to know how many checks this suite is
# supposed to run and to refuse a green verdict when fewer did.
EXPECTED_CHECKS=45
total=$((pass + fail))
if [[ "$total" -lt "$EXPECTED_CHECKS" ]]; then
  printf "  \033[31mINCOMPLETE\033[0m  only %d of %d checks ran; something above was skipped rather than failed\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
[[ "$fail" -eq 0 ]] || exit 1
echo "  LADDER ACCEPTANCE PASSED"
