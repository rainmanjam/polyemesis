#!/usr/bin/env bash
# SRTLA interop: internal/srtla against BELABOX's reference srtla_send.
#
#   scripts/srtla-interop/run.sh
#
# Builds an image with srtla_send (pinned commit, see Dockerfile) and ffmpeg,
# cross-compiles scripts/srtla-interop/rec for the Docker host, runs both in
# one container with no network (loopback only), and passes when rec reports
# that the SRT listener received the stream under the expected streamid over
# at least two links. Every container and image it makes is named
# $POLY_DOCKER_PREFIX* (default poly-) and removed on exit; set KEEP_IMAGE=1 to keep the image for a re-run.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(git -C "$HERE" rev-parse --show-toplevel)"
# Every container and image is named with this prefix, so a run can be told
# apart from (and cleaned up without touching) anything else on the host.
PREFIX="${POLY_DOCKER_PREFIX:-poly-}"
SRTLA_COMMIT="${SRTLA_COMMIT:-37862da3d0c13b46956efd3f88877053293d97d6}"
IMAGE="${PREFIX}srtla-interop:${SRTLA_COMMIT:0:12}"
NAME="${PREFIX}srtla-interop-$$"
WORK="$(mktemp -d)"

cleanup() {
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    if [ "${KEEP_IMAGE:-0}" != 1 ]; then
        docker rmi -f "$IMAGE" >/dev/null 2>&1 || true
    fi
    command rm -rf "$WORK"
}
trap cleanup EXIT

case "$(docker info --format '{{.Architecture}}')" in
    aarch64 | arm64) GOARCH=arm64 ;;
    x86_64 | amd64) GOARCH=amd64 ;;
    *) echo "unsupported docker architecture" >&2; exit 2 ;;
esac

echo "==> building rec (linux/$GOARCH)"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -o "$WORK/rec" ./scripts/srtla-interop/rec)

echo "==> building image $IMAGE (srtla $SRTLA_COMMIT)"
docker build -q --build-arg SRTLA_COMMIT="$SRTLA_COMMIT" -t "$IMAGE" -f "$HERE/Dockerfile" "$HERE" >/dev/null

echo "==> running srtla_send -> rec"
docker create --name "$NAME" --network none "$IMAGE" >/dev/null
docker cp "$WORK/rec" "$NAME:/usr/local/bin/rec" >/dev/null
status=0
docker start -a "$NAME" >"$WORK/out.log" 2>&1 || status=$?

grep -v '^RESULT ' "$WORK/out.log" | tail -40 || true
result="$(grep '^RESULT ' "$WORK/out.log" | tail -1 | cut -d' ' -f2- || true)"
echo "RESULT ${result:-<none>}"

if [ "$status" -ne 0 ] || [ -z "$result" ]; then
    echo "FAIL: srtla interop (exit $status)" >&2
    exit 1
fi
echo "PASS: srtla_send@${SRTLA_COMMIT:0:12} bonded over 2 links into internal/srtla"
