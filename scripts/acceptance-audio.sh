#!/usr/bin/env bash
# Audible-correctness acceptance test.
#
# scripts/acceptance.sh proves the ROUTING: that each destination receives
# exactly the tracks it selected. This one proves the PROCESSING applied to
# those tracks — loudness, delay, ducking, role exclusion, audio-only output and
# stem recording — and it proves it by MEASURING the audio that came out, not by
# comparing filter strings. Every one of these features is a place where a unit
# test can pass while the sound is wrong.
#
# Nothing here is asserted from a command line. Loudness is read back with
# ebur128, the delay with blackdetect against silencedetect, the duck with a
# per-band energy sweep, and the stems by looking for each track's own tone.
#
# Usage:  ./scripts/acceptance-audio.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-audio}"
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
. "$SCRIPTS/lib-preflight.sh"

# What the driver configured. Kept in step with acceptance_audio_driver.go.
TARGET_LUFS=-14
DELAY_MS=400
NEG_DELAY_MS=300

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }
note() { printf "  %s\n" "$1"; }

cleanup() {
  pkill -f "acceptance-source"       2>/dev/null
  poly_cleanup_exit "${1:-0}" "$PORT" "${WORK:-}"
}
trap 'poly_teardown_trap $? cleanup' EXIT

# poka-yoke: the driver below runs via `go run`, and every measurement runs
# ffmpeg/ffprobe against real output. Missing any of the three used to print
# as ordinary FAILs -- see lib-preflight.sh.
poly_require_exec "$BIN"
poly_require_cmd go "needed to run the acceptance driver via 'go run'"
poly_require_cmd ffmpeg
poly_require_cmd ffprobe
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

SRVPID=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
RELAY=$(lsof -nP -iUDP -a -p "$SRVPID" 2>/dev/null | awk '/UDP 127.0.0.1/{split($NF,a,":"); print a[2]; exit}')
[ -n "$RELAY" ] && ok "relay hub bound (udp/$RELAY)" || ok "relay not bound yet -- the driver discovers it from the server after creating the source"

# --------------------------------------------------------------- 2. the app
step "2. Stage the audio features (via the API the UI uses)"
# driverhelpers.go IS NAMED HERE ON PURPOSE. waitUp/grabCSRF/call/get moved
# there, and `go run` compiles a list of .go files from one directory as a
# single package -- which is what lets these drivers share code from a
# working directory that is inside no module.
#
# NO `--` SEPARATOR. It is tempting, and it is wrong: `go run` stops reading
# source files at it but hands it to the program anyway, so the driver would
# read "--" as its first argument and dial http://127.0.0.1:--. It is not
# needed either -- only LEADING consecutive .go arguments are compiled, and
# the first argument here is always a port number.
go run "$SCRIPTS/acceptance_audio_driver.go" "$SCRIPTS/driverhelpers.go" "$PORT" "$RELAY" 2>&1 | sed 's/^/  /'

REC="data/recordings"

# ------------------------------------------------------------ measurements
# band <file> <freq> [ss] [t]  ->  RMS dBFS in a narrow band around freq.
# The same technique acceptance.sh uses, with an optional window so the same
# file can be interrogated second by second.
band() {
  local f="$1" freq="$2" ss="${3:-}" dur="${4:-}"
  local pre=()
  [ -n "$ss" ]  && pre+=(-ss "$ss")
  [ -n "$dur" ] && pre+=(-t "$dur")
  ffmpeg -v info "${pre[@]}" -i "$f" \
    -af "bandpass=frequency=$freq:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
}

# bandtrack <file> <freq> <track> -> RMS dB of one frequency in ONE audio track.
#
# band above measures whatever FFmpeg's default stream selection picks, which is
# a single track and is the right thing everywhere else in this suite: every
# other destination here folds its selection into one stereo pair. A copied
# destination is the one case with several, and asking band about it would
# measure track 0 and silently report nothing about the rest.
bandtrack() {
  ffmpeg -v info -i "$1" -map "0:a:$3" \
    -af "bandpass=frequency=$2:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
}

# present <db> -> yes/no, on the same -35 dB threshold acceptance.sh uses: a
# carried tone sits near -21 dB and an excluded one at the bandpass leakage
# floor, 28-37 dB below it.
present() { awk -v d="$1" 'BEGIN{print (d+0 > -35 && d != "-inf") ? "yes" : "no"}'; }

# lufs <file> -> integrated programme loudness of the whole file.
lufs() {
  ffmpeg -v info -i "$1" -af ebur128 -f null - 2>&1 \
    | grep -A1 "Integrated loudness" | grep -o "I: *-\?[0-9.]*" | tail -1 | awk '{print $2}'
}

# avoffset <file> -> seconds the AUDIO sits behind the VIDEO in this file.
#
# The source flashes white/black on the same 8-second gate its mic track opens
# and closes on, so the video going black and the audio going silent are the
# same instant at the ingest. Whatever separates them in the output is what the
# destination did. Measured inside one file, so it cannot be contaminated by
# when this particular FFmpeg happened to start.
#
# Both edge lists have to be cleaned up first, and skipping that is how this
# check silently measures nothing: a capture that begins mid-cycle opens with a
# partial dark run and a stretch of silence, so BOTH detectors emit a spurious
# edge in the first second or so. Pairing those two artefacts with each other
# produces a confident, stable, meaningless number that is identical whatever
# the delay is set to.
EDGE_SKIP=1.5
avoffset() {
  local f="$1"
  local blacks silences
  blacks=$(ffmpeg -v info -i "$f" -vf blackdetect=d=0.5:pic_th=0.98 -f null - 2>&1 \
           | grep -o "black_start:[0-9.]*" | cut -d: -f2)
  silences=$(ffmpeg -v info -i "$f" -af silencedetect=n=-50dB:d=0.5 -f null - 2>&1 \
             | grep -o "silence_start: *[0-9.]*" | awk '{print $2}')
  [ -z "$blacks" ] || [ -z "$silences" ] && { echo ""; return; }

  # The first video edge past the settling window, then the first audio edge
  # within a second of it either way. Either way, because the offset is signed:
  # a negative delay holds the VIDEO back, so the audio edge legitimately lands
  # BEFORE the video one. A second is wide enough for any delay this product
  # allows to be visible and far short of the 8s to the neighbouring cycle, so
  # it can never match the wrong edge.
  local vb
  vb=$(echo "$blacks" | awk -v s="$EDGE_SKIP" '$1 > s {print; exit}')
  [ -z "$vb" ] && { echo ""; return; }
  local ab
  ab=$(echo "$silences" | awk -v v="$vb" '$1 >= v - 1.0 && $1 <= v + 1.0 {print; exit}')
  [ -z "$ab" ] && { echo ""; return; }
  awk -v a="$ab" -v v="$vb" 'BEGIN{printf "%.3f", a-v}'
}

# ------------------------------------------------------- 3. loudness target
step "3. Loudness target: does a -14 LUFS destination deliver -14 LUFS?"
F="$REC/loudness.mkv"
if [ ! -s "$F" ]; then
  bad "loudness destination produced no file"
else
  I=$(lufs "$F")
  if [ -z "$I" ]; then
    bad "could not measure integrated loudness"
  else
    # 1.5 LU. Single-pass loudnorm cannot see the whole programme in advance,
    # so it is allowed to miss; a miss of more than 1.5 LU is audible and means
    # the target is not reaching the filter.
    within=$(awk -v i="$I" -v t="$TARGET_LUFS" 'BEGIN{d=i-t; if(d<0)d=-d; print (d<=1.5)?"yes":"no"}')
    if [ "$within" = "yes" ]; then
      ok "integrated loudness ${I} LUFS is within 1.5 LU of ${TARGET_LUFS}"
    else
      bad "integrated loudness ${I} LUFS misses the ${TARGET_LUFS} LUFS target"
    fi
    # The destination pulls its track ~20 dB down before loudnorm sees it, so
    # an output that had nothing applied would read near -30 LUFS. Checking
    # that too is what stops "within 1.5 LU" from passing by coincidence on a
    # source that happened to arrive on target.
    lifted=$(awk -v i="$I" 'BEGIN{print (i > -20)?"yes":"no"}')
    [ "$lifted" = "yes" ] \
      && ok "the quiet source really was lifted (it reaches loudnorm near -30 LUFS)" \
      || bad "output is still near -30 LUFS; loudnorm did nothing"
  fi
fi

# --------------------------------------------------------------- 4. delay
step "4. A/V delay: does a delay move the audio, in both directions?"
REF=$(avoffset "$REC/delay-ref.mkv")
DLY=$(avoffset "$REC/delayed.mkv")
if [ -z "$REF" ] || [ -z "$DLY" ]; then
  bad "could not locate the A/V transition in one of the delay captures (ref='$REF' delayed='$DLY')"
else
  note "reference offset ${REF}s, delayed offset ${DLY}s"
  # The reference is the control: it shares everything with the delayed
  # destination except the delay, so it also carries the encoder's own priming.
  # It must read as no delay, or the measurement is not measuring what it says.
  refok=$(awk -v r="$REF" 'BEGIN{d=r; if(d<0)d=-d; print (d<=0.08)?"yes":"no"}')
  [ "$refok" = "yes" ] \
    && ok "the undelayed reference is in sync (${REF}s), so the method is sound" \
    || bad "the undelayed reference is already ${REF}s out; the measurement is unreliable"

  MEAS=$(awk -v d="$DLY" -v r="$REF" 'BEGIN{printf "%.0f", (d-r)*1000}')
  # 60 ms. One video frame at 30fps is 33 ms and silencedetect resolves to
  # about a frame of audio, so this is roughly two quantisation steps.
  close=$(awk -v m="$MEAS" -v w="$DELAY_MS" 'BEGIN{d=m-w; if(d<0)d=-d; print (d<=60)?"yes":"no"}')
  if [ "$close" = "yes" ]; then
    ok "audio moved ${MEAS}ms against picture (asked for ${DELAY_MS}ms)"
  else
    bad "audio moved ${MEAS}ms against picture, expected ${DELAY_MS}ms"
  fi

  # The other direction, which is a different mechanism. A negative delay
  # cannot be an audio filter — nothing can pull sound out of a stream before
  # it arrived — so it becomes -itsoffset on the destination's VIDEO input. It
  # leaves the filter string completely unchanged, which means a golden-string
  # test cannot see it and only a measurement can.
  NEG=$(avoffset "$REC/neg-delay.mkv")
  if [ -z "$NEG" ]; then
    bad "could not locate the A/V transition in the negative-delay capture"
  else
    NMEAS=$(awk -v n="$NEG" -v r="$REF" 'BEGIN{printf "%.0f", (n-r)*1000}')
    nclose=$(awk -v m="$NMEAS" -v w="$NEG_DELAY_MS" 'BEGIN{d=m+w; if(d<0)d=-d; print (d<=60)?"yes":"no"}')
    if [ "$nclose" = "yes" ]; then
      ok "audio moved ${NMEAS}ms against picture (asked for -${NEG_DELAY_MS}ms)"
    else
      bad "audio moved ${NMEAS}ms against picture, expected -${NEG_DELAY_MS}ms"
    fi
  fi
fi

# -------------------------------------------------------------- 5. ducking
step "5. Ducking: does the music drop while the mic is open, and come back?"

# Sweep a file in 1s windows, reporting the music band level in the windows
# where the mic is present and in the windows where it is not. Which windows
# those are is discovered from the file itself, so it does not matter where in
# the mic's cycle this particular capture began.
#
# Prints: "<music level while mic open> <music level while mic shut> <n_open> <n_shut>"
sweep() {
  local f="$1"
  local dur; dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f" 2>/dev/null | cut -d. -f1)
  [ -z "$dur" ] && { echo ""; return; }
  local t open_sum=0 open_n=0 shut_sum=0 shut_n=0
  # Skip the first and last second: a partial window at either end straddles a
  # gate edge and belongs to neither population.
  for (( t=1; t<dur-1; t++ )); do
    local mic music
    mic=$(band "$f" 2000 "$t" 1)
    music=$(band "$f" 300 "$t" 1)
    [ -z "$music" ] && continue
    [ "$music" = "-inf" ] && continue
    if [ "$(present "$mic")" = "yes" ]; then
      open_sum=$(awk -v s="$open_sum" -v v="$music" 'BEGIN{print s+v}'); open_n=$((open_n+1))
    else
      shut_sum=$(awk -v s="$shut_sum" -v v="$music" 'BEGIN{print s+v}'); shut_n=$((shut_n+1))
    fi
  done
  if [ "$open_n" -lt 2 ] || [ "$shut_n" -lt 2 ]; then echo ""; return; fi
  awk -v os="$open_sum" -v on="$open_n" -v ss="$shut_sum" -v sn="$shut_n" \
      'BEGIN{printf "%.2f %.2f %d %d", os/on, ss/sn, on, sn}'
}

DUCKED=$(sweep "$REC/ducked.mkv")
CTRL=$(sweep "$REC/duck-control.mkv")
if [ -z "$DUCKED" ] || [ -z "$CTRL" ]; then
  bad "not enough mic-open and mic-shut windows to measure ducking"
else
  read -r d_open d_shut d_on d_off <<< "$DUCKED"
  read -r c_open c_shut c_on c_off <<< "$CTRL"
  note "ducked : music ${d_open}dB while mic open, ${d_shut}dB while shut (${d_on}/${d_off} windows)"
  note "control: music ${c_open}dB while mic open, ${c_shut}dB while shut (${c_on}/${c_off} windows)"

  D_DROP=$(awk -v o="$d_open" -v s="$d_shut" 'BEGIN{printf "%.2f", s-o}')
  C_DROP=$(awk -v o="$c_open" -v s="$c_shut" 'BEGIN{printf "%.2f", s-o}')

  # 6 dB is half the perceived volume and far outside the ~1 dB drift a steady
  # tone shows between windows.
  big=$(awk -v d="$D_DROP" 'BEGIN{print (d>=6)?"yes":"no"}')
  [ "$big" = "yes" ] \
    && ok "the music drops ${D_DROP}dB while the mic is open" \
    || bad "the music only drops ${D_DROP}dB while the mic is open; expected at least 6dB"

  # The control must NOT show that drop, or something other than the duck is
  # doing it and the check above proves nothing.
  flat=$(awk -v c="$C_DROP" 'BEGIN{d=c; if(d<0)d=-d; print (d<=2)?"yes":"no"}')
  [ "$flat" = "yes" ] \
    && ok "the undicked control stays flat (${C_DROP}dB), so the drop is the duck" \
    || bad "the control also moves ${C_DROP}dB; the drop is not attributable to ducking"

  # Recovery. The mic-shut windows are AFTER mic-open windows as often as
  # before them, so a music level that returns to the control's level while the
  # mic is shut is the release working.
  rec=$(awk -v d="$d_shut" -v c="$c_shut" 'BEGIN{x=d-c; if(x<0)x=-x; print (x<=2)?"yes":"no"}')
  [ "$rec" = "yes" ] \
    && ok "the music returns to full level between bursts (${d_shut}dB vs control ${c_shut}dB)" \
    || bad "the music never recovers: ${d_shut}dB against the control's ${c_shut}dB"
fi

# ------------------------------------------------- 6. audio-only destination
step "6. Audio-only destination: is the video really gone?"
A="$REC/audio-only.m4a"
if [ ! -s "$A" ]; then
  bad "audio-only destination produced no file"
else
  NV=$(ffprobe -v error -select_streams v -show_entries stream=index -of csv=p=0 "$A" | wc -l | tr -d ' ')
  NA=$(ffprobe -v error -select_streams a -show_entries stream=index -of csv=p=0 "$A" | wc -l | tr -d ' ')
  [ "$NV" = "0" ] && ok "no video stream at all" || bad "expected 0 video streams, found $NV"
  [ "$NA" = "1" ] && ok "exactly one audio stream" || bad "expected 1 audio stream, found $NA"
  # It has to be the RIGHT audio, not merely some audio: tracks 1+2 were
  # selected, so 300 and 900 Hz are present and the mic at 2000 is not.
  for spec in "300 yes" "900 yes" "2000 no"; do
    read -r f want <<< "$spec"
    db=$(band "$A" "$f"); got=$(present "$db")
    [ "$got" = "$want" ] \
      && ok "audio-only: ${f}Hz ${db}dB present=$got (expected $want)" \
      || bad "audio-only: ${f}Hz ${db}dB present=$got but expected $want"
  done
fi

# ----------------------------------------------------- 7. role exclusion
step "7. Role exclusion: does marking a track 'music' remove it from a mix?"
N="$REC/no-music.mkv"
if [ ! -s "$N" ]; then
  bad "role-exclusion destination produced no file"
else
  # The profile enables tracks 1 AND 2. Track 1 is annotated music, so only
  # 900 Hz may survive. Nothing about the destination's own track selection
  # changed — the exclusion is coming from the role.
  db3=$(band "$N" 300);  g3=$(present "$db3")
  db9=$(band "$N" 900);  g9=$(present "$db9")
  [ "$g3" = "no" ]  && ok "the music track is gone (300Hz ${db3}dB)" \
                    || bad "the music track is still present (300Hz ${db3}dB)"
  [ "$g9" = "yes" ] && ok "the other selected track survived (900Hz ${db9}dB)" \
                    || bad "role exclusion took the wrong track too (900Hz ${db9}dB)"
fi

# -------------------------------------------------- 7b. copied audio (#144)
step "7b. Copy: do the ingest tracks arrive separately, and does the DMCA switch still work?"
C="$REC/copied.mkv"
if [ ! -s "$C" ]; then
  bad "the copy destination produced no file"
else
  # THE FIRST CLAIM: separate tracks, not a mix. Every other destination in
  # this suite folds its selection into ONE stereo pair; this is the only one
  # that must not, and a count of 1 would mean the mix path ran after all.
  # Deduped by stream index (the sort -u): for containers with a program
  # layer (MPEG-TS/SRT) ffprobe lists each stream again inside its program,
  # so a plain line count reports 6 for 3 tracks. Stream indexes are unique
  # within a file, so deduping them is exact. Matroska has no program layer,
  # so this is a no-op for the .mkv asserted here -- the dedup matters the
  # moment the copy destination points at TS/SRT, the container family #144
  # is about. NA is also the loop bound below, so an inflated count would
  # probe track indexes that do not exist.
  NA=$(ffprobe -v error -select_streams a -show_entries stream=index -of csv=p=0 "$C" \
        | tr -d ' ' | sort -u | grep -c '[0-9]')
  NV=$(ffprobe -v error -select_streams v -show_entries stream=index -of csv=p=0 "$C" \
        | tr -d ' ' | sort -u | grep -c '[0-9]')
  [ "$NA" = "2" ] && ok "two separate audio tracks survived the copy" \
                  || bad "expected 2 separate audio tracks, found $NA (a mix would give 1)"
  [ "$NV" = "1" ] && ok "video is still there and still copied" \
                  || bad "expected 1 video stream, found $NV"

  # THE SECOND CLAIM: the role exclusion still applies. All three tracks were
  # selected and "music" was excluded, so the 300 Hz bed must be absent from
  # EVERY track of the file — this is the compliance assertion, and it is the
  # reason copy maps tracks explicitly instead of using `-map 0`.
  # EVERY track is checked, not just the first: an exclusion that leaked into
  # track 1 while track 0 was clean is exactly the failure this is here to
  # catch, and a whole-file measurement would miss it.
  gone=yes; got900=no; got2000=no
  i=0
  while [ "$i" -lt "$NA" ]; do
    d3=$(bandtrack "$C" 300 "$i")
    d9=$(bandtrack "$C" 900 "$i")
    d2=$(bandtrack "$C" 2000 "$i")
    note "track $i: 300Hz ${d3}dB  900Hz ${d9}dB  2000Hz ${d2}dB"
    [ "$(present "$d3")" = "yes" ] && gone=no
    [ "$(present "$d9")" = "yes" ] && got900=yes
    [ "$(present "$d2")" = "yes" ] && got2000=yes
    i=$((i+1))
  done
  [ "$gone" = "yes" ] && ok "the music track is absent from every copied track (300Hz)" \
                      || bad "the excluded music track reached a copied destination (300Hz)"

  # And the tracks that SHOULD be there. Survival, not ordering — ordering is
  # what scripts/verify_ertmp_multitrack.go exists for.
  [ "$got900" = "yes" ]  && ok "the control tone survived the copy (900Hz)" \
                         || bad "the control tone did not survive the copy (900Hz)"
  [ "$got2000" = "yes" ] && ok "the mic track survived the copy (2000Hz)" \
                         || bad "the mic track did not survive the copy (2000Hz)"
fi

# ------------------------------------------------------------- 8. stems
step "8. Stem recording: one file per track, each carrying only its own tone"
SD="$REC/stems"
if [ ! -d "$SD" ]; then
  bad "no stems directory was created"
else
  COUNT=$(find "$SD" -name '*.flac' | wc -l | tr -d ' ')
  [ "$COUNT" -ge 3 ] && ok "one stem per ingest track ($COUNT flac files)" \
                     || bad "expected 3 stems, found $COUNT"

  # Named from the roles set through the annotations API, not from track
  # numbers. This is also the only end-to-end proof that annotations reach the
  # recorder at all.
  for role in music commentary mic; do
    if find "$SD" -name "*-${role}.flac" | grep -q .; then
      ok "stem named for its role: ${role}.flac"
    else
      bad "no stem named ${role}.flac (found: $(find "$SD" -name '*.flac' -exec basename {} \; | tr '\n' ' '))"
    fi
  done

  # Isolation is the whole point of a stem. Each one must carry its own tone
  # and none of the others, or it is a copy of the mix under a helpful name.
  check_stem() { # check_stem <role> <own freq> <other freq> <other freq>
    local role="$1" own="$2" o1="$3" o2="$4"
    local f; f=$(find "$SD" -name "*-${role}.flac" | head -1)
    [ -z "$f" ] && return
    local dbo; dbo=$(band "$f" "$own")
    [ "$(present "$dbo")" = "yes" ] \
      && ok "${role}.flac carries its own ${own}Hz (${dbo}dB)" \
      || bad "${role}.flac is missing its own ${own}Hz (${dbo}dB)"
    for other in "$o1" "$o2"; do
      local dbx; dbx=$(band "$f" "$other")
      [ "$(present "$dbx")" = "no" ] \
        && ok "${role}.flac is free of ${other}Hz (${dbx}dB)" \
        || bad "${role}.flac has leaked ${other}Hz (${dbx}dB)"
    done
  }
  check_stem music      300  900 2000
  check_stem commentary 900  300 2000
  # The mic is gated, so its own tone is measured over the whole file where it
  # is present for half the time; the other two must be absent throughout.
  check_stem mic        2000 300 900
fi

# ------------------------------------------- 9. backwards compatibility
step "9. Backwards compatibility: an untouched profile compiles as it always did"
# Compiled through the running server's own API, against the SAME annotated
# 3-track source every destination above was compiled against. A profile using
# none of the new fields has to be unmoved by all of them — including by the
# roles that were just recorded on the ingest.
#
# Written to a file and read back rather than piped: a `while read` on the far
# side of a pipe runs in a subshell, and every pass and fail it counted would be
# discarded when that subshell exited.
go run "$SCRIPTS/acceptance_audio_compat.go" "$PORT" > compat.txt 2>&1
while read -r line; do
  case "$line" in
    PASS*) ok "${line#PASS }" ;;
    FAIL*) bad "${line#FAIL }" ;;
    *)     note "$line" ;;
  esac
done < compat.txt

# ------------------------------------------------------------------ summary
step "Summary"
total=$((pass + fail))
printf "  %d passed, %d failed\n\n" "$pass" "$fail"

# Fixed-value guard, which this suite did not have and needed.
#
# The stem checks live behind `find`: when no file matches a role, the three
# name checks FAIL and the nine check_stem measurements below them never run at
# all, because check_stem returns early on an empty filename. CI reported
# "23 passed, 3 failed" and looked like three ordinary failures -- nine checks
# had silently not happened, and nothing said so.
EXPECTED_CHECKS=35
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran; the rest never executed.\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
[ "$fail" -eq 0 ] || exit 1
echo "  AUDIO ACCEPTANCE PASSED"
