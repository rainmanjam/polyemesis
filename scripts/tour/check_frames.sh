#!/usr/bin/env bash
# check_frames.sh <video>   OCR one frame every 0.5 s; exit 1 listing the times where a forbidden
# string appears, 2 when tesseract is missing (a skipped check must not read as a pass).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VIDEO="${1:-}"
[ -n "$VIDEO" ] || { echo "usage: check_frames.sh <video>" >&2; exit 2; }
command -v tesseract >/dev/null 2>&1 || { echo "tesseract is not installed" >&2; exit 2; }
command -v ffmpeg >/dev/null 2>&1 || { echo "ffmpeg is not installed" >&2; exit 2; }
[ -f "$VIDEO" ] || { echo "check_frames.sh: $VIDEO does not exist" >&2; exit 2; }

FORBIDDEN=("Nothing is live" "No chat account" "not connected" "YouTube · YouTube" "No platform accounts connected")
DIR="$HERE/check"
mkdir -p "$DIR"
command rm -f "$DIR"/f*.png "$DIR"/f*.txt

ffmpeg -y -loglevel error -i "$VIDEO" -vf fps=2 "$DIR/f%05d.png"

hits=0
for png in "$DIR"/f*.png; do
  n="$(basename "$png" .png)"; n="${n#f}"
  tesseract "$png" "${png%.png}" >/dev/null 2>&1 || true
  txt="$(tr -s '[:space:]' ' ' < "${png%.png}.txt" 2>/dev/null || true)"
  for s in "${FORBIDDEN[@]}"; do
    if grep -qiF -- "$s" <<<"$txt"; then
      t="$(awk -v n="$((10#$n))" 'BEGIN{printf "%.1f", (n-1)*0.5}')"
      echo "$t s: \"$s\""
      hits=$((hits+1))
    fi
  done
done

if [ "$hits" -gt 0 ]; then
  echo "check_frames.sh: $hits forbidden string(s) found in $VIDEO" >&2
  exit 1
fi
echo "check_frames.sh: no forbidden string in $VIDEO"
