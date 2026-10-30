#!/bin/sh
# Runs inside the interop container (see run.sh). Everything shares the
# container's loopback: rec (receiver + SRT listener) on 127.0.0.1, and
# srtla_send bonding over two source addresses, 127.0.0.1 and 127.0.0.2 --
# all of 127/8 is bindable on Linux, so the receiver sees two distinct links
# without any routing between Docker networks.
set -eu
STREAMID="${STREAMID:-interop-token}"
DURATION="${DURATION:-25}"

printf '127.0.0.1\n127.0.0.2\n' >/tmp/ips

rec -listen :5000 -upstream 127.0.0.1:6000 -streamid "$STREAMID" \
    -min-bytes "${MIN_BYTES:-2000000}" -links 2 -timeout "${TIMEOUT:-60s}" &
REC=$!
sleep 1

srtla_send 7000 127.0.0.1 5000 /tmp/ips >/tmp/srtla_send.log 2>&1 &
SEND=$!
sleep 2

ffmpeg -hide_banner -loglevel error -nostdin -re \
    -f lavfi -i "testsrc=size=1280x720:rate=30" -t "$DURATION" \
    -c:v mpeg2video -b:v 4M -maxrate 4M -bufsize 2M \
    -f mpegts "srt://127.0.0.1:7000?streamid=${STREAMID}&pkt_size=1316&latency=500000" \
    >/tmp/ffmpeg.log 2>&1 &
FF=$!

status=0
wait "$REC" || status=$?
kill "$FF" "$SEND" 2>/dev/null || true
if [ "$status" -ne 0 ]; then
    echo "--- srtla_send log (tail)"; tail -20 /tmp/srtla_send.log || true
    echo "--- ffmpeg log (tail)"; tail -20 /tmp/ffmpeg.log || true
fi
exit "$status"
