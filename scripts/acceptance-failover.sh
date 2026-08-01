#!/usr/bin/env bash
# Failover acceptance: a destination rides a source switch without noticing.
#
# The source-selector tier's whole promise is that the hub a destination reads
# never changes -- only the bytes arriving on it do. Switching a destination's
# own subscription would restart its process and drop the platform connection,
# which is the exact failure failover exists to prevent.
#
# The engine's design notes name the two things that decide whether that works,
# and BOTH were covered only against fakes:
#
#   PTS CONTINUITY. Each feed normalises its input to a timeline starting at
#   zero, so without -output_ts_offset every switch hands the destinations a
#   timestamp that jumps BACKWARDS by however long the last feed ran. A platform
#   answers a backwards jump by dropping the connection. Nothing in a unit test
#   produces a real timestamp, so nothing in a unit test can catch this.
#
#   RIDING THE SWITCH. The destination process must not restart. "The file has
#   bytes in it" would pass just as happily on a destination that died and came
#   back, so this suite counts restarts rather than inspecting output alone.
#
# Both are measured here against real FFmpeg, real timestamps and a real
# publisher that is killed and brought back.
#
# Usage:  ./scripts/acceptance-failover.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-failover}"
PORT=8096
# RTMP, not SRT: the host FFmpeg (Homebrew) is built without libsrt, so it can
# neither listen nor publish on SRT. SRT ingest is covered by the container
# suites. What this suite measures -- the timeline across a feed swap -- is
# the same either way.
INGEST=1938
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
# Shared observation. See lib-observe.sh: a wait that gives up has to report
# what it saw, or a primary that arrived 200ms late reads exactly like one that
# never arrived. Issue #38.
. "$SCRIPTS/lib-observe.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

cleanup() {
  pkill -f "failover-publisher" 2>/dev/null
  # INGEST is passed because the ingest ffmpeg's argv carries no work-dir path,
  # so the sweep above cannot see it. It is the port that leaked.
  poly_cleanup "$PORT" "$WORK" "$INGEST"
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg is required"; exit 1; }
rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
mkdir -p data/recordings

# Built once rather than `go run` per call. This suite polls status on a half
# second cadence through three waits, and a fresh compile each time would make
# the poll interval meaningless -- the switch would be measured against the Go
# toolchain's speed rather than the tier's.
DRIVER="$WORK/failover-driver"
go build -o "$DRIVER" "$SCRIPTS/acceptance_failover_driver.go" || {
  echo "cannot build the driver"; exit 1; }
drive() { "$DRIVER" "http://127.0.0.1:$PORT" "$@" 2>&1; }

# publish starts an encoder against the ingest, standing in for OBS. Named so
# cleanup can find it, since it outlives any single check.
publish() {
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=1000:sample_rate=48000" \
    -metadata comment=failover-publisher \
    -map 0:v -map 1:a -c:v libx264 -preset ultrafast -g 30 -pix_fmt yuv420p \
    -b:v 1200k -c:a aac -b:a 128k -ac 2 -shortest -t "${1:-3600}" \
    -f flv "rtmp://127.0.0.1:$INGEST/live" \
    > "publisher-$RANDOM.log" 2>&1 &
}
unpublish() { pkill -f "failover-publisher" 2>/dev/null; }

# publisher_postmortem says whether the encoder standing in for OBS is even
# alive, and what it last said.
#
# A primary that never goes on air is far more often a publisher that failed to
# connect than a selector that failed to switch -- and the publisher's own log
# is the only place that distinction is written down. Without this, both look
# identical from the status line, which is how issue #38's failure came to be
# reported as "the primary never went on air" when nothing had established that
# the primary was ever publishing.
publisher_postmortem() {
  local f
  if pgrep -f "failover-publisher" >/dev/null 2>&1; then
    note "the publisher process is still running"
  else
    note "the publisher process is GONE -- it exited early or never started"
  fi
  for f in publisher-*.log; do
    [ -f "$f" ] || continue
    [ -s "$f" ] || { note "$f is empty (ffmpeg said nothing)"; continue; }
    note "$f (last 5 lines):"
    tail -5 "$f" | sed 's/^/          /'
  done
  note "server.log (last 8 lines):"
  tail -8 server.log 2>/dev/null | sed 's/^/          /'
}

# waitfor polls the driver's status line until a field matches, or times out.
# The status line is "<active> <switches> <primaryLive> <destRestarts>".
#   waitfor <field-number> <wanted> <seconds>
#
# Delegates to poly_poll_field so a timeout prints the whole trajectory rather
# than returning a bare 1. The caller used to recover context by sampling
# readstatus AGAIN after the deadline, which reports the state at the moment of
# the post-mortem and not the state during the wait -- the two are different
# readings and only the second one answers "was it close?".
waitfor() {
  local idx="$1" want="$2" secs="$3" name
  case "$idx" in
    1) name="the active feed" ;;
    2) name="the switch count" ;;
    3) name="primaryLive" ;;
    4) name="the destination restart count" ;;
    *) name="field $idx" ;;
  esac
  poly_poll_field "$name to become $want" "$idx" "$want" "$secs" readstatus
}

# readstatus returns a status line with all four fields, retrying a read that
# came back malformed.
#
# The server answers /status while it is swapping a feed, and a truncated body
# there is a transient, not a verdict. Taking it at face value once turned a
# clean run into "the destination restarted (0 -> unexpected)" -- an alarming
# and completely false failure. A check must not report a product fault because
# its own read was short.
readstatus() {
  local i line
  for i in 1 2 3 4 5; do
    line=$(drive status 2>/dev/null | tail -1)
    # Four fields, the last of which is a number: anything else is a partial
    # read or an error message that must not reach the arithmetic below.
    if [ "$(printf '%s' "$line" | wc -w | tr -d ' ')" = "4" ] &&
       printf '%s' "$line" | awk '{exit !($4 ~ /^-?[0-9]+$/)}'; then
      printf '%s\n' "$line"
      return 0
    fi
    sleep 0.5
  done
  printf 'unreadable 0 false -1\n'
}

step "1. Server, with failover and a slate"
"$BIN" -addr ":$PORT" -data ./data -log info > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
grep -q "polyemesis" server.log && ok "server started" || { bad "server did not start"; exit 1; }

OUT=$(drive)
case "$OUT" in *SETUP_OK*)    ok "first-run setup" ;;          *) bad "setup: $OUT"; exit 1 ;; esac
case "$OUT" in *FAILOVER_OK*) ok "failover enabled with a slate" ;; *) bad "failover: $OUT"; exit 1 ;; esac
case "$OUT" in *DEST_OK*)     ok "one destination on the selector" ;; *) bad "dest: $OUT"; exit 1 ;; esac

step "2. The primary goes on air"
# Before publishing, not after. The server reporting ready means its HTTP
# listener is up; the ingest child is spawned after that and binds 1938 a moment
# later. A publisher that arrives first is refused and exits, and the wait below
# then spends its whole 40s ceiling on a primary that was never coming.
poly_wait_port_ready "$INGEST" 15 || true
publish
if waitfor 1 primary 40 ; then
  ok "the primary is on air once it delivers"
else
  bad "the primary never went on air within 40s"
  publisher_postmortem
  exit 1
fi
# Let a few seconds of real primary land in the file before anything is cut.
sleep 6
BEFORE=$(readstatus)
set -- $BEFORE; restarts_before="${4:-0}"
note "before the cut: $BEFORE"

step "3. The encoder disappears — the slate takes over"
unpublish
# Grace is 2s; allow generously for the sweep and the feed swap.
if waitfor 1 slate 30 ; then
  ok "the slate went on air after the grace period"
else
  bad "the slate never took over within 30s"
  publisher_postmortem
fi
sleep 6

step "4. The encoder returns — auto-return puts the primary back"
publish
if waitfor 1 primary 40 ; then
  ok "the primary was restored automatically"
else
  bad "auto-return never happened: $(readstatus)"
fi
sleep 6

AFTER=$(readstatus)
set -- $AFTER; active_after="$1"; switches="${2:-0}"; restarts_after="${4:-0}"
note "after the cycle: $AFTER"

step "5. THE POINT: the destination never restarted"
# This is the whole feature. If the destination restarted, the platform
# connection dropped and failover achieved nothing -- the output file could
# still look perfectly healthy.
if [ "$restarts_before" = "-1" ] || [ "$restarts_after" = "-1" ]; then
  bad "no destination process was reported; nothing was measured"
elif [ "$restarts_after" -eq "$restarts_before" ]; then
  ok "the destination rode both switches without restarting ($restarts_after restarts)"
else
  bad "the destination restarted across the switch ($restarts_before -> $restarts_after)"
  note "a restart drops the platform connection, which is what failover exists to prevent"
fi

if [ "${switches:-0}" -ge 2 ]; then
  ok "the tier recorded both switches ($switches)"
else
  bad "expected at least 2 switches, saw ${switches:-0}"
fi

step "6. Filler starts playing, with the encoder still on air"
# THE CASE THIS WHOLE SUB-PROJECT EXISTS FOR, and it was impossible to write
# before it. A playing file used to feed the PRIMARY's hub, so the primary
# always had bytes on it, the selector read the programme as live, and it would
# never switch to a backup or a slate. Playing a file silently disabled the
# entire failover feature -- everything steps 2 to 5 just measured turned itself
# off the first time anybody put a file on air, and nothing said so.
#
# The playlist is enabled HERE rather than in step 1 deliberately: it outranks
# the slate, so a tier running from the start would have taken the slate's place
# in step 3 and this suite would have stopped measuring the cycle it was
# originally written for.
FILLER_TONE=2000
# Same geometry, frame rate and codec as the publisher above. The destination
# copies video, so filler that did not match would be measuring a platform's
# tolerance for a mid-stream codec change rather than the selector's switch.
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=640x360:rate=30" \
  -f lavfi -i "sine=frequency=$FILLER_TONE:sample_rate=48000" \
  -map 0:v -map 1:a -c:v libx264 -preset ultrafast -g 30 -pix_fmt yuv420p \
  -b:v 1200k -c:a aac -b:a 128k -ac 2 -t 8 \
  -y data/recordings/filler.ts 2>/dev/null
[ -s data/recordings/filler.ts ] || { bad "could not build the filler clip"; exit 1; }
OUT=$(drive playlist recordings/filler.ts)
case "$OUT" in *PLAYLIST_OK*) : ;; *) bad "enable playlist: $OUT"; exit 1 ;; esac

# The pin is how this suite SEES the file delivering while the encoder is still
# connected, and it has to see that before cutting the encoder or the step below
# proves nothing about filler. A pin is honoured only while its source is
# actually delivering, so the playlist becoming active IS the evidence that its
# own hub is carrying bytes. There is no status field to read instead, and "the
# tier is running" would be the wrong question anyway: a path that is confined
# but names no file leaves FFmpeg backing off forever with the process reported
# healthy the whole time.
OUT=$(drive pin playlist)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin playlist: $OUT"; exit 1 ;; esac
if waitfor 1 playlist 40 ; then
  ok "the filler is on air while the encoder is still publishing"
else
  # Not fatal. The checks below say what happened when the encoder dropped, and
  # a run that stopped here would report a cause nobody had measured -- the
  # habit issue #38 exists to break.
  bad "the filler never went on air, so nothing was playing when the encoder dropped"
  publisher_postmortem
fi

OUT=$(drive pin auto)
case "$OUT" in *PIN_OK*) : ;; *) bad "pin auto: $OUT"; exit 1 ;; esac
if waitfor 1 primary 40 ; then
  ok "a live encoder pre-empts the filler as soon as the pin is released"
else
  bad "the primary never came back after the pin was released: $(readstatus)"
fi
sleep 4

step "7. THE POINT: the encoder drops while the filler plays"
unpublish
# THE REGRESSION, in one field. Before the playlist got a hub of its own, the
# file's bytes landed on the PRIMARY's relay, so primaryLive stayed true for as
# long as the file played -- with no encoder connected at all -- and every
# failover decision underneath it became unreachable.
if waitfor 3 false 30 ; then
  ok "the primary is seen to go down even though a file is on air"
else
  bad "primaryLive stayed true with no encoder connected -- the filler is feeding the primary's hub"
  publisher_postmortem
fi
if waitfor 1 playlist 30 ; then
  ok "the selector left the dead primary and put the filler on air"
else
  bad "the selector never switched away from the primary while the filler played"
fi

FILLER_STATUS=$(readstatus)
set -- $FILLER_STATUS; restarts_filler="${4:-0}"
note "with the filler on air: $FILLER_STATUS"
# Same measurement as step 5, against the same baseline: a switch that restarts
# a destination drops the platform connection, and it does that whether the
# source arriving is a slate or a scheduled programme.
if [ "$restarts_filler" = "-1" ]; then
  bad "no destination process was reported across the filler switch"
elif [ "$restarts_filler" -eq "$restarts_before" ]; then
  ok "the destination rode the switch to filler without restarting ($restarts_filler restarts)"
else
  bad "the destination restarted when the filler went on air ($restarts_before -> $restarts_filler)"
fi

step "8. The output timeline"
drive stopall >/dev/null 2>&1
sleep 8
OUTFILE=data/recordings/onair.mkv
if [ ! -s "$OUTFILE" ]; then
  bad "no output was produced, so the timeline could not be measured"
else
  ok "the destination produced a continuous output across the whole cycle"

  # TIMELINE MONOTONICITY. A feed started without -output_ts_offset republishes
  # from zero, so the switch hands the destination a timestamp behind the one
  # before it, and a platform answers a backwards jump by dropping the
  # connection. Counting backwards steps is the direct measurement of the flag
  # working.
  #
  # DTS, NOT PTS. The first version of this check measured pts_time and reported
  # 268 backwards steps on a stream that was completely healthy. Presentation
  # timestamps are not required to be monotonic in packet order -- frame
  # reordering means a correct encoder emits them out of order routinely.
  # DECODE timestamps are the ones that must never go backwards, and they are
  # what a receiving platform's demuxer trips over. Measured against the same
  # file: 268 backwards PTS steps, 0 backwards DTS steps.
  back=$(ffprobe -v error -select_streams v -show_entries packet=dts_time \
         -of csv=p=0 "$OUTFILE" 2>/dev/null |
         awk -F, 'BEGIN{prev=-1e9;n=0} {if ($1=="N/A") next; t=$1+0; if (t < prev - 0.001) n++; prev=t} END{print n+0}')
  if [ "${back:-1}" -eq 0 ]; then
    ok "no backwards decode timestamp across either switch"
  else
    bad "$back backwards DTS step(s) — a platform drops the connection on these"
  fi

  # The switch must be visible IN THE BYTES, not only in the status field. The
  # publisher sends a 1 kHz tone and the slate is silent, so a file that carries
  # both means the destination really was fed from two different sources.
  quiet=$(ffmpeg -hide_banner -nostats -i "$OUTFILE" \
          -af "silencedetect=n=-50dB:d=1" -f null - 2>&1 | grep -c "silence_start" || true)
  if [ "${quiet:-0}" -ge 1 ]; then
    ok "the slate period is present in the audio (tone -> silence -> tone)"
  else
    bad "the output never went quiet; the slate was not actually on air"
  fi

  # Span from the last decode timestamp rather than format=duration. A
  # destination stopped mid-write leaves an unfinalised Matroska whose header
  # carries no duration at all -- ffprobe answers "N/A", which is not zero and
  # not a failure, and comparing it as a number is how the first version of this
  # check produced "the output is only N/As".
  dur=$(ffprobe -v error -select_streams v -show_entries packet=dts_time \
        -of csv=p=0 "$OUTFILE" 2>/dev/null |
        awk -F, '{if ($1!="N/A") last=$1+0} END{printf "%d", last}')
  if [ "${dur:-0}" -ge 15 ]; then
    ok "the output spans the whole down-and-back cycle (${dur}s)"
  else
    bad "the output spans only ${dur:-0}s; it did not survive the cycle"
  fi
fi

step "Summary"
printf "  %d passed, %d failed\n\n" "$pass" "$fail"
# Fixed-value guard. Several checks live behind an "if the file exists" branch,
# so a suite that fell over early could otherwise report "all passed" having
# measured almost nothing.
#
# Raised from 12 to 17 by the filler case in steps 6 and 7: five checks, and
# every one of them lives after a `bad ... exit 1` precondition, so a run that
# could not build the clip or enable the tier has to be told apart from one that
# measured the case and liked what it saw.
EXPECTED_CHECKS=17
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran\n\n" "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$fail" -eq 0 ]; then
  printf "  \033[32mFAILOVER ACCEPTANCE PASSED\033[0m\n\n"
else
  printf "  \033[31mFAILOVER ACCEPTANCE FAILED\033[0m\n\n"
fi
[ "$fail" -eq 0 ]
