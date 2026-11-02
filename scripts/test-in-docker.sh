#!/usr/bin/env bash
# Run Go tests against the FFmpeg that SHIPS, rather than the one on your laptop.
#
# This exists because the two disagree in a way that silently weakens the test
# suite. A Homebrew FFmpeg on macOS is built without libfreetype, so it has no
# `drawtext` filter at all -- 481 filters and none of them that one. Every
# pixel-measurement test for a text overlay therefore SKIPS on that machine, and
# a green local run means nothing.
#
# Alpine 3.24 carries FFmpeg 8.1.3, the same version the project pins, built
# WITH libfreetype. So this is not "some other FFmpeg" -- it is the same release
# with the feature compiled in, and it is what the Docker image actually ships.
#
# One more thing it proves, which no local run can: the shipping image has
# **zero font files**. fontconfig is installed and finds nothing, so
# `drawtext=text=hi` fails with "Cannot find a valid font for the family Sans".
# Anything that draws text has to carry its own font, and this is the only
# environment where forgetting that shows up.
#
# Usage:
#   ./scripts/test-in-docker.sh                     # every package
#   ./scripts/test-in-docker.sh ./internal/ffmpeg/  # one package
#   ./scripts/test-in-docker.sh ./internal/ffmpeg/ -run TestOverlay
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# THE GO VERSION COMES FROM go.mod, not from this file.
#
# This script used to say golang:1.26-alpine. go.mod moved to 1.27, and the
# official golang images set GOTOOLCHAIN=local, so the container refused to
# build the module at all ("go.mod requires go >= 1.27.0") -- and nothing
# noticed, because nothing runs this script in CI. So: the `toolchain` directive
# if go.mod has one (the exact release the project builds with), else the `go`
# directive (the floor).
GO_VERSION="$(awk '$1 == "toolchain" { sub(/^go/, "", $2); print $2; exit }' "$ROOT/go.mod")"
[ -n "$GO_VERSION" ] || GO_VERSION="$(awk '$1 == "go" { print $2; exit }' "$ROOT/go.mod")"
[ -n "$GO_VERSION" ] || { echo "could not read a go or toolchain directive from $ROOT/go.mod"; exit 1; }

# THE DIGEST COMES FROM THE ROOT Dockerfile, which pins its golang base as
# tag@sha256 (internal/testenv/dockerfile_digest_pin_test.go) and which
# dependabot keeps current. Borrowing that pin means this script builds from
# the same bytes the release does and never carries a digest of its own to go
# stale. If the Dockerfile's tag is not the version go.mod asks for, the two
# have drifted; say so and use the bare tag rather than a digest for the wrong
# release.
BASE="golang:${GO_VERSION}-alpine"
PINNED="$(sed -nE 's/^FROM[[:space:]]+(--platform=[^[:space:]]+[[:space:]]+)?(golang:[^[:space:]@]+@sha256:[0-9a-f]{64}).*/\2/p' "$ROOT/Dockerfile" | head -1)"
if [ "${PINNED%@*}" = "$BASE" ]; then
  BASE="$PINNED"
else
  echo "warning: go.mod wants $BASE but the root Dockerfile pins ${PINNED:-no golang image};" >&2
  echo "         using the unpinned tag. Bump the Dockerfile's golang FROM to match go.mod." >&2
fi
# The digest is in the cache key, so a dependabot bump rebuilds the image.
DIGEST="${BASE##*@sha256:}"
[ "$DIGEST" = "$BASE" ] && DIGEST="unpinned"
IMAGE="polyemesis-test:go${GO_VERSION}-${DIGEST:0:12}-ffmpeg"

command -v docker >/dev/null || { echo "docker is required"; exit 1; }

# Built once and cached. The apk index is the slow part, and rebuilding it per
# run would make this too slow to reach for.
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "building $IMAGE from $BASE (first run only)..."
  docker build -q --build-arg "BASE=$BASE" -t "$IMAGE" - <<'DOCKERFILE' >/dev/null
ARG BASE
FROM ${BASE}
# ffmpeg for the measurement tests; the rest is what cgo-free Go needs to build
# and what the acceptance drivers shell out to.
RUN apk add --no-cache ffmpeg git bash
# NOT installed on purpose: a font package.
#
# The shipping image has none, and a test that draws text has to prove it works
# there rather than on a machine that happens to have fonts lying around. If a
# test needs a font it must supply one, exactly as the product must.
WORKDIR /src
DOCKERFILE
  [ "${PIPESTATUS[0]}" -eq 0 ] || { echo "building $IMAGE failed"; exit 1; }
fi

TARGET="${1:-./...}"
shift 2>/dev/null || true

echo "ffmpeg in the container:"
docker run --rm "$IMAGE" ffmpeg -hide_banner -version 2>/dev/null | head -1 | sed 's/^/  /'
printf '  drawtext: '
docker run --rm "$IMAGE" sh -c 'ffmpeg -hide_banner -filters 2>/dev/null | grep -qE " drawtext " && echo present || echo ABSENT'
echo

# GOFLAGS=-count=1 defeats the test cache: a cached PASS from a previous run
# would defeat the entire point of running here.
exec docker run --rm \
  -v "$ROOT:/src" \
  -v "$ROOT/.docker-gocache:/root/.cache/go-build" \
  -v "$ROOT/.docker-gomod:/go/pkg/mod" \
  -e GOFLAGS=-count=1 \
  -e CGO_ENABLED=0 \
  "$IMAGE" go test "$TARGET" "$@"
