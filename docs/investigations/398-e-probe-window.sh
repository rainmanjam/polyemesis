set -e
cd /tmp
echo "=== ffmpeg $(ffmpeg -version 2>&1 | head -1 | awk '{print $3}') ==="
# ONE stable source, no parameter change at all, no concatenation: this tests
# the probe window alone.
ffmpeg -hide_banner -loglevel error -y -re \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=300:sample_rate=48000" \
  -f lavfi -i "sine=frequency=900:sample_rate=48000" \
  -map 0:v -map 1:a -map 2:a -c:v libx264 -preset veryfast -g 60 -pix_fmt yuv420p \
  -b:v 2000k -c:a aac -b:a 128k -t 120 -f mpegts "udp://127.0.0.1:23997?pkt_size=1316" &
PUB=$!
sleep 2

now() { awk '{print $1}' /proc/uptime; }

# The destination argv, read out of a running container, plus whatever extra
# input options $1 names. Backgrounded; the caller owns the PID in $D.
start_dest() {
  rm -f out.mkv
  # shellcheck disable=SC2086 -- $1 is a deliberately split option list
  ffmpeg -hide_banner -nostdin -loglevel error -nostats \
    -fflags +genpts -thread_queue_size 1024 -analyzeduration 15000000 -probesize 33554432 $1 \
    -i "udp://127.0.0.1:23997?fifo_size=32768&overrun_nonfatal=1" \
    -filter_complex "[0:a:0]pan=stereo|c0=1*c0|c1=1*c0[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c0[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]" \
    -map 0:v:0 -c:v copy -map "[aout]" -c:a aac -b:a 160k -ac 2 -ar 48000 \
    -f matroska out.mkv 2>/dev/null &
  D=$!
}

# TIME TO FIRST OUTPUT, the question the first version of this script did not
# ask. It polls the file rather than signalling the process, because a signal
# is exactly what the first version got wrong (see below).
first_output() { # extra-input-options label
  s=$(now)
  start_dest "$1"
  while :; do
    sz=$(stat -c %s out.mkv 2>/dev/null || echo 0)
    [ "$sz" -gt 100000 ] && break
    kill -0 "$D" 2>/dev/null || { echo "  $2: consumer EXITED with $sz bytes"; return; }
    [ "$(awk "BEGIN{print ($(now)-$s > 30)}")" = 1 ] && { echo "  $2: nothing after 30s"; break; }
    sleep 0.1
  done
  printf "  %-34s first 100 KB after %5.1fs\n" "$2" "$(awk "BEGIN{print $(now)-$s}")"
  kill -KILL "$D" 2>/dev/null || true
  wait "$D" 2>/dev/null || true
}

# The original question: does a destination STOPPED inside the window write
# only a header? It does not -- but read the result for what it is. SIGTERM
# interrupts avformat_find_stream_info, and FFmpeg then opens the output and
# writes what it had buffered. So media in the file says the SIGNAL flushed the
# probe, not that the probe had already ended. The first version of this script
# read it as "FFmpeg starts writing long before the ceiling", which is false,
# and the time-to-first-output rows above are what show it.
run_for() { # seconds label
  start_dest ""
  sleep "$1"
  kill -TERM "$D" 2>/dev/null || true
  wait "$D" 2>/dev/null || true
  printf "  %-28s %8s bytes  " "$2" "$(stat -c %s out.mkv 2>/dev/null || echo 0)"
  ffprobe -v error -show_entries format=duration -of csv=p=0 out.mkv 2>/dev/null | head -1 || echo "unreadable"
  echo
}

echo "--- time to first output (the ffmpeg CLI forces scan_all_pmts=1) ---"
first_output "" "as built before (scan_all_pmts=1)"
first_output "-scan_all_pmts 0" "with -scan_all_pmts 0"
echo "--- stopped with SIGTERM INSIDE the 15s window ---"
run_for 8 "stopped at 8s"
echo "--- stopped just AFTER it ---"
run_for 20 "stopped at 20s"
kill $PUB 2>/dev/null || true
