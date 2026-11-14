#!/usr/bin/env bash
# check_frames.sh <video>   OCR one frame every 0.5 s; exit 1 listing the times where a forbidden
# string appears. Exit 2 whenever the check could not run properly (no tesseract, no frames, an
# OCR failure, text read in fewer than half of the frames): a skipped or broken check must never read as a pass.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VIDEO="${1:-}"
[ -n "$VIDEO" ] || { echo "usage: check_frames.sh <video>" >&2; exit 2; }
command -v tesseract >/dev/null 2>&1 || { echo "tesseract is not installed" >&2; exit 2; }
command -v ffmpeg >/dev/null 2>&1 || { echo "ffmpeg is not installed" >&2; exit 2; }
[ -s "$VIDEO" ] || { echo "check_frames.sh: $VIDEO is missing or empty" >&2; exit 2; }

# Fixed phrases, then one pattern (ERE, case-insensitive): OCR reads the middle dot of
# "YouTube · YouTube" as ".", "-", ":" or nothing at all ("YouTubeYouTube"). The fixed phrases are matched with a frame's
# lines joined (they wrap); the pattern is matched within one line.
FORBIDDEN=("Nothing is live" "No chat account" "not connected" "No platform accounts connected")
DOUBLE='youtube[^[:alnum:]]{0,3}youtube'

DIR="$HERE/check/ocr"
mkdir -p "$DIR"
command rm -f "$DIR"/f*.png "$DIR"/f*.txt

if ! ffmpeg -y -loglevel error -i "$VIDEO" -vf fps=2 "$DIR/f%05d.png"; then
  echo "check_frames.sh: ffmpeg could not read frames from $VIDEO" >&2
  exit 2
fi
shopt -s nullglob
frames=("$DIR"/f*.png)
if [ "${#frames[@]}" -eq 0 ]; then
  echo "check_frames.sh: no frame was extracted from $VIDEO" >&2
  exit 2
fi

hits=0
nonempty=0
for png in "${frames[@]}"; do
  n="$(basename "$png" .png)"; n="${n#f}"
  if ! tesseract "$png" "${png%.png}" >/dev/null 2>&1; then
    echo "check_frames.sh: tesseract failed on $png" >&2
    exit 2
  fi
  if [ ! -f "${png%.png}.txt" ]; then
    echo "check_frames.sh: tesseract wrote no text file for $png" >&2
    exit 2
  fi
  txt="$(tr -s '[:space:]' ' ' < "${png%.png}.txt")"
  case "$txt" in *[![:space:]]*) nonempty=$((nonempty+1)) ;; esac
  t="$(awk -v n="$((10#$n))" 'BEGIN{printf "%.1f", (n-1)*0.5}')"
  for s in "${FORBIDDEN[@]}"; do
    if grep -qiF -- "$s" <<<"$txt"; then
      echo "$t s: \"$s\""
      hits=$((hits+1))
    fi
  done
  # One label on one line, so this one is matched line by line. With the lines joined, a
  # platform chip ("YouTube") on one row followed by a destination name ("YouTube · Español")
  # on the next reads as the forbidden label.
  if grep -qiE -- "$DOUBLE" "${png%.png}.txt"; then
    echo "$t s: \"YouTube · YouTube\""
    hits=$((hits+1))
  fi
done

# Text in at least half of the frames, not in one: a broken OCR that reads a single frame
# must not read as a pass. (The tour has text on screen for nearly all of its length.)
if [ $((nonempty * 2)) -lt "${#frames[@]}" ]; then
  echo "check_frames.sh: text was read from only $nonempty of ${#frames[@]} frames of $VIDEO" >&2
  exit 2
fi
if [ "$hits" -gt 0 ]; then
  echo "check_frames.sh: $hits forbidden string(s) found in $VIDEO" >&2
  exit 1
fi
echo "check_frames.sh: no forbidden string in $VIDEO"
