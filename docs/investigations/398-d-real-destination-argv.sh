set -e
cd /tmp
echo "=== ffmpeg $(ffmpeg -version 2>&1 | head -1 | awk '{print $3}') ==="
mk() { # w h fps out
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=${1}x${2}:rate=${3}:duration=12" \
    -f lavfi -i "sine=frequency=300:sample_rate=48000:duration=12" \
    -f lavfi -i "sine=frequency=900:sample_rate=48000:duration=12" \
    -map 0:v -map 1:a -map 2:a \
    -c:v libx264 -preset veryfast -profile:v high -level 4.0 -g "$((2*$3))" \
    -pix_fmt yuv420p -b:v 3000k -c:a aac -b:a 128k -f mpegts "$4"
}
mk 1280 720 60 mis.ts
mk 1920 1080 30 fill.ts
cat mis.ts fill.ts mis.ts > mixed.ts

ffmpeg -hide_banner -loglevel error -y -re -i mixed.ts -c copy -f mpegts \
  "udp://127.0.0.1:23998?pkt_size=1316" &
PUB=$!
sleep 1.5

# THE EXACT DESTINATION COMMAND polyemesis builds, read out of a running
# container: video copied, audio through the routing filtergraph, 32MB probe.
timeout 75 ffmpeg -hide_banner -nostdin -loglevel warning -nostats \
  -fflags +genpts -thread_queue_size 1024 -analyzeduration 15000000 -probesize 33554432 \
  -i "udp://127.0.0.1:23998?fifo_size=32768&overrun_nonfatal=1" \
  -filter_complex "[0:a:0]pan=stereo|c0=1*c0|c1=1*c0[a_t0];[0:a:1]pan=stereo|c0=1*c0|c1=1*c0[a_t1];[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];[a_mix]alimiter=limit=0.95:level=disabled[a_norm];[a_norm]aresample=48000:async=1:first_pts=0[aout]" \
  -map 0:v:0 -c:v copy -map "[aout]" -c:a aac -b:a 160k -ac 2 -ar 48000 \
  -f matroska out.mkv 2>&1 | head -14; echo "  (destination exit=$?)"
wait $PUB 2>/dev/null || true

echo "out.mkv: $(stat -c %s out.mkv 2>/dev/null || echo MISSING) bytes"
echo -n "declares: "; ffprobe -v error -select_streams v:0 -show_entries stream=width,height,r_frame_rate -of csv=p=0 out.mkv 2>/dev/null | head -1
echo -n "duration: "; ffprobe -v error -show_entries format=duration -of csv=p=0 out.mkv 2>/dev/null | head -1
