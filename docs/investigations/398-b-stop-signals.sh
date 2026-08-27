set -e
cd /tmp
V=$(ffmpeg -version 2>&1 | head -1 | awk '{print $3}')
echo "=== ffmpeg $V ==="
mk() { # w h fps out
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=${1}x${2}:rate=${3}:duration=6" \
    -f lavfi -i "sine=frequency=1000:sample_rate=48000:duration=6" \
    -map 0:v -map 1:a -c:v libx264 -preset veryfast -profile:v high -level 4.0 \
    -g "$((2*$3))" -pix_fmt yuv420p -b:v 3000k -c:a aac -b:a 128k -ac 2 \
    -f mpegts "$4"
}
mk 1920 1080 30 a.ts
mk 1280 720 60 b.ts
cat a.ts b.ts a.ts > mixed.ts

run_and_kill() { # signal label
  rm -f out.mkv
  ffmpeg -hide_banner -loglevel error -re -i mixed.ts -c copy -f matroska out.mkv &
  P=$!
  sleep 8
  kill -"$1" "$P" 2>/dev/null || true
  wait "$P" 2>/dev/null || true
  SZ=$(stat -c %s out.mkv 2>/dev/null || echo 0)
  D=$(ffprobe -v error -show_entries format=duration -of csv=p=0 out.mkv 2>/dev/null | head -1)
  W=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 out.mkv 2>/dev/null | head -1)
  echo "  $2: ${SZ} bytes  duration=${D:-none}  declares=${W:-none}"
}
echo "--- stopped mid-stream, after the resolution change ---"
run_and_kill KILL  "SIGKILL "
run_and_kill TERM  "SIGTERM "
run_and_kill INT   "SIGINT  "
