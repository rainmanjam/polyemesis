#!/usr/bin/env bash
# Browser end-to-end, against the shipped container.
#
# This is the layer the project had none of. Four real UI bugs were found by
# hand in a single session -- a hook called after an early return, a PUT
# carrying server-computed fields so every save silently reverted, a source
# created disabled, and port fields committing on blur so tabbing out of one
# dropped the stream. Not one of them is visible to `tsc`, and all four are
# trivially assertable in a browser.
#
# It runs against the CONTAINER rather than a Vite dev server, so what is tested
# is the artefact a user pulls: embedded assets, the Go router's SPA fallback,
# the real API.
#
# Usage:  ./scripts/acceptance-browser.sh
# Requires: docker, node
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"

IMAGE=polyemesis:browser
CTR=poly-browser
VOL=poly-browser-data
PORT=8099

cleanup() {
  docker rm -f "$CTR" >/dev/null 2>&1
  docker volume rm "$VOL" >/dev/null 2>&1
}
trap cleanup EXIT
cleanup

command -v docker >/dev/null || { echo "docker not found"; exit 1; }
docker info >/dev/null 2>&1 || { echo "docker daemon not running"; exit 1; }

printf "\n\033[1m1. Build and start the shipped image\033[0m\n"
if ! docker build -t "$IMAGE" --build-arg VERSION=browser "$ROOT" >/tmp/poly-browser-build.log 2>&1; then
  echo "  image build failed (see /tmp/poly-browser-build.log)"
  tail -12 /tmp/poly-browser-build.log
  exit 1
fi
echo "  image built"

docker volume create "$VOL" >/dev/null 2>&1
# No ingest ports published: this suite never streams, and binding them would
# collide with any other polyemesis running on the machine.
docker run -d --name "$CTR" -p "$PORT:8080" -v "$VOL:/data" "$IMAGE" >/dev/null 2>&1

for _ in $(seq 1 60); do
  [ "$(docker inspect --format '{{.State.Health.Status}}' "$CTR" 2>/dev/null)" = "healthy" ] && break
  sleep 1
done
if [ "$(docker inspect --format '{{.State.Health.Status}}' "$CTR" 2>/dev/null)" != "healthy" ]; then
  echo "  container never became healthy"
  docker logs "$CTR" 2>&1 | tail -15
  exit 1
fi
echo "  container healthy on :$PORT"

printf "\n\033[1m2. Browser suite\033[0m\n"
cd "$ROOT/ui"
BASE_URL="http://127.0.0.1:$PORT" npx --no-install playwright test --config e2e/playwright.config.ts
status=$?

if [ "$status" -eq 0 ]; then
  printf "\n\033[32mBROWSER ACCEPTANCE PASSED\033[0m\n\n"
else
  printf "\n\033[31mBROWSER ACCEPTANCE FAILED\033[0m\n\n"
  echo "server log:"
  docker logs "$CTR" 2>&1 | grep -viE "insecure exposure|whisper" | tail -20
fi
exit "$status"
