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
. "$SCRIPTS/lib-preflight.sh"

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

# poka-yoke: this suite's own header says "Requires: docker, node" but node
# was never checked -- see lib-preflight.sh.
poly_require_docker
poly_require_cmd node "needed to run 'npx playwright'"
poly_require_cmd npx "needed to run the playwright suite in ui/"

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
cd "$ROOT/ui" || exit 1
# auth.setup.ts creates the admin account with this and then signs in with it,
# so the value never leaves this process and is generated fresh per run. A
# literal in the repository would be a committed password regardless of how
# short-lived the account it guards is, so auth.setup.ts refuses to start
# without the variable rather than carrying a default.
#
# openssl is checked rather than assumed: an empty command substitution would
# leave a four-character password that fails the eight-character floor, and the
# suite would fail at sign-in rather than at the missing tool.
if [ -z "${E2E_PASSWORD:-}" ]; then
  command -v openssl >/dev/null 2>&1 || \
    poly_preflight_fail "openssl is needed to generate E2E_PASSWORD; export one yourself instead"
  E2E_PASSWORD="E2E-$(openssl rand -hex 16)"
fi
export E2E_PASSWORD
# The container name, for the one spec that has to reach past the API: the
# playlist editor's "needing attention" case needs an upload to go missing
# while a saved item still names it, and the API refuses to create that state
# on purpose (409). It removes the file with `docker exec`, which is that
# suite's os.Remove -- see removeUploadOutOfBand in e2e/playlist-editor.spec.ts.
export E2E_CONTAINER="$CTR"
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
