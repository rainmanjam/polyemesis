set -e
cd /tmp
echo "=== ffmpeg $(ffmpeg -version 2>&1 | head -1 | awk '{print $3}') ==="
mk() {
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=${1}x${2}:rate=${3}:duration=6" \
    -f lavfi -i "sine=frequency=1000:sample_rate=48000:duration=6" \
    -map 0:v -map 1:a -c:v libx264 -preset veryfast -profile:v high -level 4.0 \
    -g "$((2*$3))" -pix_fmt yuv420p -b:v 3000k -c:a aac -b:a 128k -ac 2 \
    -f mpegts "$4"
}
# THE SUITE'S ORDER: the destination starts while the MISMATCH geometry is on
# air, then the selector cuts to the 1080p30 filler.
mk 1280 720 60 mis.ts
mk 1920 1080 30 fill.ts
cat mis.ts fill.ts mis.ts > mixed.ts

# Over UDP, which is what a destination actually reads: unseekable, and the
# muxer must commit to a header from the first packets it sees.
ffmpeg -hide_banner -loglevel error -y -re -i mixed.ts -c copy -f mpegts \
  "udp://127.0.0.1:23999?pkt_size=1316" &
PUB=$!
sleep 0.4
timeout 14 ffmpeg -hide_banner -loglevel warning -y \
  -fflags +genpts -i "udp://127.0.0.1:23999?fifo_size=32768&overrun_nonfatal=1" \
  -c copy -f matroska out.mkv 2>&1 | head -8 || true
wait $PUB 2>/dev/null || true
echo "out.mkv: $(stat -c %s out.mkv 2>/dev/null || echo MISSING) bytes"
echo -n "declares: "
ffprobe -v error -select_streams v:0 -show_entries stream=width,height,r_frame_rate -of csv=p=0 out.mkv 2>/dev/null | head -1
echo -n "duration: "
ffprobe -v error -show_entries format=duration -of csv=p=0 out.mkv 2>/dev/null | head -1
