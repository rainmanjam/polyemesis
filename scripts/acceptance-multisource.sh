#!/usr/bin/env bash
# Multi-source acceptance test.
#
# The scenario that forced sources to exist: OBS's vertical-canvas plugin emits
# a horizontal and a vertical feed that are two different compositions, not one
# cropped from the other. Before this, one install could ingest one of them.
#
# This proves two programmes run side by side in ONE container, each with its
# own ingest port and its own destinations, and that their audio never crosses.
# The proof is by measurement: each source is fed a different tone, and each
# destination's output is bandpassed at both frequencies. A destination that
# carried the other programme's audio would show it.
#
# Usage:  ./scripts/acceptance-multisource.sh
# Requires: docker, go
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$SCRIPTS/acceptance_docker_driver.go"

IMAGE=polyemesis:multisource
CTR=poly-ms
NET=poly-ms-net
VOL=poly-ms-data
PORT=8097
BASE="http://127.0.0.1:$PORT"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

cleanup() {
  docker rm -f "$CTR" pub-h pub-v >/dev/null 2>&1
  docker volume rm "$VOL" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

drive() { go run "$DRIVER" "$@" 2>&1; }
inctr() { docker exec "$CTR" sh -c "$1" 2>/dev/null; }

# publish <name> <port> <freq> <seconds>
# One video and one audio track at a distinctive frequency, so the programme a
# destination received can be identified afterwards rather than assumed.
publish() {
  docker run -d --rm --name "$1" --network "$NET" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=$3:sample_rate=48000" \
    -map 0:v -map 1:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v 1000k \
    -c:a aac -b:a 128k -ac 2 -t "$4" \
    -f mpegts "srt://$CTR:$2?mode=caller&latency=200000&transtype=live" >/dev/null 2>&1
}

# publish_token <name> <sharedPort> <token> <freq> <seconds>
# Addresses a programme by putting its publish token in the SRT streamid, which
# is what OBS does. FFmpeg exposes libsrt's streamid directly in the URL.
publish_token() {
  docker run -d --rm --name "$1" --network "$NET" --entrypoint ffmpeg "$IMAGE" \
    -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=640x360:rate=30" \
    -f lavfi -i "sine=frequency=$4:sample_rate=48000" \
    -map 0:v -map 1:a \
    -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -b:v 1000k \
    -c:a aac -b:a 128k -ac 2 -t "$5" \
    -f mpegts "srt://$CTR:$2?mode=caller&latency=200000&transtype=live&streamid=$3" >/dev/null 2>&1
}

rms() {
  inctr "cd /data/recordings && ffmpeg -hide_banner -nostats -i $1 \
    -af 'bandpass=f=$2:width_type=h:w=$3,astats=metadata=1:reset=0' -f null - 2>&1 \
    | grep 'RMS level dB' | tail -1 | sed 's/.*: *//'"
}
louder_than() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a+0 > b+0)}'; }

docker info >/dev/null 2>&1 || { echo "docker daemon not running"; exit 1; }

step "1. Image and container"
docker build -t "$IMAGE" --build-arg VERSION=multisource "$ROOT" >/tmp/poly-ms-build.log 2>&1 \
  && ok "image builds" || { bad "image build (see /tmp/poly-ms-build.log)"; exit 1; }

docker network create "$NET" >/dev/null 2>&1
docker volume create "$VOL" >/dev/null 2>&1
# Both ingest ports published. 6001 is where the second source lands once the
# server moves it off the first source's 6000.
docker run -d --name "$CTR" --network "$NET" \
  -p "$PORT:8080" -p "6000:6000/udp" -p "6001:6001/udp" -p "6100:6100/udp" \
  -v "$VOL:/data" "$IMAGE" >/dev/null 2>&1

healthy=no
for _ in $(seq 1 60); do
  [ "$(docker inspect --format '{{.State.Health.Status}}' "$CTR" 2>/dev/null)" = "healthy" ] && { healthy=yes; break; }
  sleep 1
done
[ "$healthy" = yes ] && ok "container healthy" || { bad "container never became healthy"; exit 1; }

step "2. Two sources in one install"
drive setup "$BASE" >/dev/null

# The migration made the first source from the existing ingest. Adding a second
# is the whole feature.
read -r VID VPORT <<<"$(drive addsource "$BASE" Vertical | tail -1)"
if [ -n "${VID:-}" ] && [ -n "${VPORT:-}" ]; then
  ok "second source created (id $VID)"
else
  bad "could not create a second source"; exit 1
fi

# There are no per-source ports to collide any more. What has to be distinct is
# the TOKEN, because that is now the only thing that tells the two apart -- two
# sources sharing one would make the second unreachable.
TOKENS=$(drive tokens "$BASE")
TOK1=$(echo "$TOKENS" | awk '$1==1 {print $2}')
TOK2=$(echo "$TOKENS" | awk -v id="$VID" '$1==id {print $2}')
if [ -n "$TOK1" ] && [ -n "$TOK2" ] && [ "$TOK1" != "$TOK2" ]; then
  ok "each source has its own publish token"
else
  bad "tokens missing or identical; one of the programmes has no address"
fi

# And NO ingest listener should be running: SRT is served by the in-process Go
# listener now, so an FFmpeg listener here would be a second thing on the same
# socket -- which is exactly the collision this change removed.
# The bracket keeps the pattern from matching the `sh -c` that carries it --
# without it this counts its own command line and never reads zero.
INGESTS=$(inctr 'ps -o args | grep -c "mode=lis[t]ener"' | tr -d ' ')
# Zero is this check's PASSING value, so `${INGESTS:-0}` defaulted to the pass:
# a docker exec that produced nothing -- container gone, daemon busy, `ps`
# missing from the image -- read as "no competing listener". Three outcomes,
# not two. The count has to have been TAKEN before its value means anything.
case "$INGESTS" in
  ''|*[!0-9]*)
    bad "could not count ingest listeners in the container (ps returned '$INGESTS')" ;;
  0)
    ok "no FFmpeg SRT listener competing with the one-port server" ;;
  *)
    bad "$INGESTS FFmpeg SRT listener(s) running; they cannot all hold port 6000" ;;
esac

step "3. A destination on each source"
# Source 1 is the one the migration created from the existing ingest.
drive destfor "$BASE" 1 horizontal-out horiz.mkv 0 >/dev/null
drive destfor "$BASE" "$VID" vertical-out vert.mkv 0 >/dev/null
ok "one file destination on each source"

step "4. Publish a different programme into each"
# 300 Hz into the horizontal source, 5000 Hz into the vertical one. Two tones
# far enough apart that a bandpass cannot confuse them -- and BOTH on port 6000,
# told apart only by the token in the streamid.
publish_token pub-h 6000 "$TOK1" 300 30
publish_token pub-v 6000 "$TOK2" 5000 30
sleep 34
drive stopall "$BASE" >/dev/null
sleep 12

H3=$(rms horiz.mkv 300 100);  H50=$(rms horiz.mkv 5000 400)
V3=$(rms vert.mkv 300 100);   V50=$(rms vert.mkv 5000 400)

if [ -n "$H3" ] && [ -n "$V50" ]; then
  printf "        horizontal out  300Hz %s   5000Hz %s\n" "$H3" "$H50"
  printf "        vertical   out  300Hz %s   5000Hz %s\n" "$V3" "$V50"

  # Each destination carries its own programme...
  louder_than "$H3" "-45"  && ok "horizontal destination carries its own 300 Hz programme" \
                           || bad "horizontal destination is missing its programme (300Hz $H3)"
  louder_than "$V50" "-45" && ok "vertical destination carries its own 5000 Hz programme" \
                           || bad "vertical destination is missing its programme (5000Hz $V50)"

  # ...and NOT the other one. This is the assertion the whole feature rests on:
  # if the two programmes shared a relay hub, each output would contain both.
  if louder_than "$H3" "$(awk -v x="$H50" 'BEGIN{print x+20}')"; then
    ok "horizontal destination did NOT receive the vertical programme"
  else
    bad "programmes crossed: horizontal out contains 5000Hz at $H50 against its own $H3"
  fi
  if louder_than "$V50" "$(awk -v x="$V3" 'BEGIN{print x+20}')"; then
    ok "vertical destination did NOT receive the horizontal programme"
  else
    bad "programmes crossed: vertical out contains 300Hz at $V3 against its own $V50"
  fi
else
  bad "could not measure the outputs (no recordings produced)"
fi

step "5. Survives a container replacement"
docker rm -f "$CTR" >/dev/null 2>&1
docker run -d --name "$CTR" --network "$NET" \
  -p "$PORT:8080" -p "6000:6000/udp" -p "6001:6001/udp" -p "6100:6100/udp" \
  -v "$VOL:/data" "$IMAGE" >/dev/null 2>&1
sleep 14
# Count the SOURCES, not listener processes. Counting FFmpeg listeners was how
# this used to work, and after the one-port change there are none -- but the
# pattern matched the `sh -c` carrying it, so the check went on passing for a
# reason that had nothing to do with persistence. A check that cannot fail is
# worse than one that is missing, because it reads as coverage.
AFTER=$(drive tokens "$BASE" | grep -c . || true)
if [ "${AFTER:-0}" -ge 2 ] 2>/dev/null; then
  ok "both sources came back after the container was destroyed ($AFTER sources)"
else
  bad "only $AFTER source(s) after restart; a source did not persist"
fi

step "6. The listener can be moved, and both programmes follow it"
# Step 4 already proved token routing on the default port. What this adds is
# that the port is configuration rather than a constant: an operator who has to
# avoid 6000 can move it, and every source moves with it because none of them
# owns a port of its own.
O=$(drive oneport "$BASE" 6100)
case "$O" in *ONEPORT_OK*) ok "one-port SRT listener enabled" ;;
             *) bad "could not enable one-port ingest: $O" ;; esac
sleep 4

# "<id> <token>" per source.
TOKENS=$(drive tokens "$BASE")
TOK1=$(echo "$TOKENS" | awk '$1==1 {print $2}')
TOK2=$(echo "$TOKENS" | awk -v id="$VID" '$1==id {print $2}')
if [ -n "$TOK1" ] && [ -n "$TOK2" ]; then
  ok "both sources have a publish token"
else
  bad "could not read publish tokens"
fi

# Fresh outputs so this section measures its own traffic.
drive destfor "$BASE" 1 h-oneport h1.mkv 0 >/dev/null
drive destfor "$BASE" "$VID" v-oneport v1.mkv 0 >/dev/null
drive startall "$BASE" >/dev/null

# 300 Hz to the horizontal token, 5000 Hz to the vertical one -- SAME PORT.
publish_token pub-h 6100 "$TOK1" 300 30
publish_token pub-v 6100 "$TOK2" 5000 30
sleep 34
drive stopall "$BASE" >/dev/null
sleep 12

P3=$(rms h1.mkv 300 100);  P50=$(rms h1.mkv 5000 400)
Q3=$(rms v1.mkv 300 100);  Q50=$(rms v1.mkv 5000 400)
if [ -n "$P3" ] && [ -n "$Q50" ]; then
  printf "        horizontal (token) 300Hz %s   5000Hz %s\n" "$P3" "$P50"
  printf "        vertical   (token) 300Hz %s   5000Hz %s\n" "$Q3" "$Q50"
  louder_than "$P3" "-45"  && ok "token-addressed horizontal received its programme" \
                           || bad "horizontal token delivered nothing (300Hz $P3)"
  louder_than "$Q50" "-45" && ok "token-addressed vertical received its programme" \
                           || bad "vertical token delivered nothing (5000Hz $Q50)"
  # The assertion that matters: one port, and the two programmes still never mix.
  if louder_than "$P3" "$(awk -v x="$P50" 'BEGIN{print x+20}')"; then
    ok "on ONE port, the horizontal programme did not receive the vertical one"
  else
    bad "programmes crossed on the shared port: horizontal has 5000Hz at $P50 against its own $P3"
  fi
  if louder_than "$Q50" "$(awk -v x="$Q3" 'BEGIN{print x+20}')"; then
    ok "on ONE port, the vertical programme did not receive the horizontal one"
  else
    bad "programmes crossed on the shared port: vertical has 300Hz at $Q3 against its own $Q50"
  fi
else
  bad "could not measure the one-port outputs"
fi

# A wrong token must be refused rather than landing anywhere.
docker rm -f pub-bad >/dev/null 2>&1
if publish_token pub-bad 6100 "not-a-real-token" 1000 6; then sleep 9; fi
BADLOG=$(docker logs "$CTR" 2>&1 | grep -c "token not recognised")
if [ "${BADLOG:-0}" -ge 1 ] 2>/dev/null; then
  ok "an unrecognised token was refused"
else
  bad "an unrecognised token was not refused"
fi

printf "\n\033[1m%d passed, %d failed\033[0m\n" "$pass" "$fail"

# Fixed-value guard: confirm the COUNT, not just the verdict.
#
# This suite's later checks depend on two publishers actually connecting, and
# every one of them is inside a conditional. A run where neither published still
# reaches this line and prints "0 failed", which reads as success -- the exact
# way the postprod suite once reported "7 passed, 0 failed -- PASSED" having
# skipped five checks.
EXPECTED_CHECKS=18
total=$((pass + fail))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  only %d of %d checks ran; the run stopped early\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$total" -gt "$EXPECTED_CHECKS" ]; then
  printf "  \033[33mNOTE\033[0m  %d checks ran, %d expected. If checks were added,\n" \
    "$total" "$EXPECTED_CHECKS"
  printf "        raise EXPECTED_CHECKS so the guard keeps its teeth.\n"
fi
[ "$fail" -eq 0 ]
