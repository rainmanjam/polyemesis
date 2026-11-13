#!/usr/bin/env bash
# publish.sh          encode scripts/tour/out/tour-master.mp4 into the site's two media files
# publish.sh --check  verify those files against web/src/data/tour.mjs and chapters.json; writes nothing
#
# Writes exactly two files: web/public/media/tour.mp4 and web/public/media/tour-poster.jpg.
# Overrides, for testing: TOUR_MASTER, TOUR_MP4, TOUR_POSTER, TOUR_DATA, TOUR_CHAPTERS.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
MASTER="${TOUR_MASTER:-$HERE/out/tour-master.mp4}"
MP4="${TOUR_MP4:-$REPO/web/public/media/tour.mp4}"
POSTER="${TOUR_POSTER:-$REPO/web/public/media/tour-poster.jpg}"
DATA="${TOUR_DATA:-$REPO/web/src/data/tour.mjs}"
CHAPTERS="${TOUR_CHAPTERS:-$HERE/chapters.json}"
MAX_BYTES=8000000

need() { command -v "$1" >/dev/null 2>&1 || { echo "publish.sh: $1 is not installed" >&2; exit 2; }; }
need ffmpeg; need ffprobe; need node

size_of() { wc -c < "$1" | tr -d ' '; }

check() {
  local bad=0
  fail() { echo "publish.sh --check: $1" >&2; bad=1; }

  if [ ! -f "$MP4" ]; then
    fail "$MP4 is missing"
  else
    local bytes streams dims
    bytes="$(size_of "$MP4")"
    [ "$bytes" -le "$MAX_BYTES" ] || fail "$MP4 is $bytes bytes, over $MAX_BYTES"
    streams="$(ffprobe -v error -show_entries stream=codec_type -of csv=p=0 "$MP4")"
    case "$streams" in *audio*) fail "$MP4 has an audio stream; the tour is silent" ;; esac
    dims="$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=s=x:p=0 "$MP4")"
    [ "$dims" = "1920x1080" ] || fail "$MP4 is $dims, not 1920x1080"
  fi
  [ -f "$POSTER" ] || fail "$POSTER is missing"

  if [ -f "$MP4" ]; then
    local actual
    actual="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$MP4")"
    node -e '
      const { pathToFileURL } = require("node:url");
      import(pathToFileURL(process.argv[1]).href).then((m) => {
        const want = m.TOUR.duration, got = Number(process.argv[2]);
        if (!(Math.abs(want - got) <= 0.5)) {
          console.error("publish.sh --check: the mp4 lasts " + got.toFixed(2) + " s but TOUR.duration is " + want + " (tolerance 0.5 s)");
          process.exit(1);
        }
      });' "$DATA" "$actual" || bad=1
  fi

  # Titles: tour.mjs holds the display form ("One stream in"); chapters.json holds the
  # on-video form ("01 · ONE STREAM IN"). They match when the number prefix is dropped
  # and case is ignored. Starts must be equal.
  node -e '
    const { pathToFileURL } = require("node:url");
    const fs = require("node:fs");
    const norm = (t) => t.replace(/^\d+\s*·\s*/, "").trim().toLowerCase();
    import(pathToFileURL(process.argv[1]).href).then((m) => {
      const site = m.TOUR.chapters, file = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
      let ok = site.length === file.length;
      if (!ok) console.error("publish.sh --check: tour.mjs has " + site.length + " chapters, chapters.json has " + file.length);
      for (let i = 0; ok && i < site.length; i++) {
        if (site[i].start !== file[i].start || norm(site[i].title) !== norm(file[i].title)) {
          console.error("publish.sh --check: chapter " + (i + 1) + " differs: tour.mjs " + JSON.stringify(site[i]) + " vs chapters.json " + JSON.stringify(file[i]));
          ok = false;
        }
      }
      process.exit(ok ? 0 : 1);
    });' "$DATA" "$CHAPTERS" || bad=1

  return "$bad"
}

poster_second() {
  local s
  s="$(awk '/^## Poster/{f=1;next} f&&NF{print;exit}' "$HERE/README.md" | sed -nE 's/.*frame at ([0-9]+(\.[0-9]+)?) s.*/\1/p')"
  [ -n "$s" ] || { echo "publish.sh: cannot read the poster second from the first line of the \"Poster\" section of $HERE/README.md" >&2; exit 1; }
  echo "$s"
}

case "${1:-}" in
  --check)
    if check; then echo "publish.sh --check: ok"; exit 0; fi
    exit 1 ;;
  "") ;;
  *) echo "usage: publish.sh [--check]" >&2; exit 2 ;;
esac

if [ ! -f "$MASTER" ]; then
  echo "publish.sh: $MASTER is missing. Render the HyperFrames project first (see $HERE/README.md)." >&2
  exit 1
fi
SECOND="$(poster_second)"

TMP_MP4="$MP4.tmp.mp4"; TMP_JPG="$POSTER.tmp.jpg"
trap 'command rm -f "$TMP_MP4" "$TMP_JPG"' EXIT
chosen=""
for crf in 20 22 24 26 28 30; do
  ffmpeg -y -loglevel error -i "$MASTER" -an -vf scale=1920:1080 -c:v libx264 -profile:v high -pix_fmt yuv420p \
    -preset slow -crf "$crf" -movflags +faststart "$TMP_MP4"
  bytes="$(size_of "$TMP_MP4")"
  echo "crf $crf: $bytes bytes"
  if [ "$bytes" -le "$MAX_BYTES" ]; then chosen="$crf"; break; fi
done
if [ -z "$chosen" ]; then
  echo "publish.sh: no crf from 20 to 30 fits in $MAX_BYTES bytes; nothing was written" >&2
  exit 1
fi
ffmpeg -y -loglevel error -ss "$SECOND" -i "$MASTER" -frames:v 1 -vf scale=1920:1080 -q:v 3 "$TMP_JPG"
mv "$TMP_MP4" "$MP4"; mv "$TMP_JPG" "$POSTER"
echo "wrote $MP4 (crf $chosen) and $POSTER (frame at $SECOND s)"
echo "now verifying against $DATA and chapters.json:"
check && echo "publish.sh: ok"
