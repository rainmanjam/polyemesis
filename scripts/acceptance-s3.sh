#!/usr/bin/env bash
# S3 offload against a real MinIO.
#
# Starts MinIO in a container, then runs the s3integration-tagged Go tests in
# internal/objstore (SigV4 round trip, the server refusing a body that is not
# the signed digest, a wrong secret refused and never echoed) and
# internal/offload (move mode: upload, verify, evict, fetch back; the Test
# button's put/get/delete). Unit CI never needs Docker: those files only build
# with -tags s3integration, and they FAIL rather than skip without the
# endpoint, so a run that reached MinIO is the only green this prints.
#
# The image is Chainguard's MinIO pinned by digest. Docker Hub's minio/minio
# refused anonymous pulls when this was written, and quay.io answered 401.
#
# Usage:  ./scripts/acceptance-s3.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="cgr.dev/chainguard/minio@sha256:0a01100085e30fb08d2f39e39c9f5c9d83a95707f220b007d813b690cd41622d"
CONTAINER="polyemesis-acceptance-minio"
PORT="${POLYEMESIS_S3_PORT:-19000}"
# Obviously fake, low-entropy credentials for a throwaway local container.
ACCESS="polytestuser"
SECRET="polytestpassword"

command -v docker >/dev/null 2>&1 || { echo "FAIL: docker is required (MinIO runs as a container)" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "FAIL: go is required" >&2; exit 1; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "== starting MinIO ($IMAGE) on 127.0.0.1:$PORT"
docker run -d --name "$CONTAINER" -p "127.0.0.1:$PORT:9000" \
	-e MINIO_ROOT_USER="$ACCESS" -e MINIO_ROOT_PASSWORD="$SECRET" \
	"$IMAGE" server /tmp/data >/dev/null

ready=""
for _ in $(seq 1 60); do
	if docker logs "$CONTAINER" 2>&1 | grep -q "API:"; then
		ready=1
		break
	fi
	if [ "$(docker inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null || echo gone)" != running ]; then
		break
	fi
	sleep 1
done
if [ -z "$ready" ]; then
	echo "FAIL: MinIO did not come up; its log follows" >&2
	docker logs "$CONTAINER" >&2 || true
	exit 1
fi

echo "== go test -tags s3integration"
cd "$ROOT"
out="$(mktemp)"
status=0
POLYEMESIS_S3_ENDPOINT="http://127.0.0.1:$PORT" \
	POLYEMESIS_S3_ACCESS_KEY="$ACCESS" POLYEMESIS_S3_SECRET_KEY="$SECRET" \
	go test -race -count=1 -tags s3integration -run 'Live' -v -timeout 5m \
	./internal/objstore/ ./internal/offload/ >"$out" 2>&1 || status=$?
grep -E '^(=== RUN|--- |ok|FAIL|panic)' "$out" || true

# A mistyped -run exits 0 having run nothing; insist on the cases by name.
for want in TestLiveRoundTripWithAKeyThatNeedsEscaping TestLiveServerRefusesABodyThatIsNotTheSignedDigest \
	TestLiveWrongSecretIsRefusedAndNeverEchoed TestLiveMoveModeUploadsVerifiesEvictsAndFetchesBack \
	TestLiveProbeRunsPutGetDelete TestLiveMultipartAndRangedGet \
	TestLiveMultipartResumesAcrossARestartAndFetchesBack; do
	if ! grep -q -- "--- PASS: $want " "$out"; then
		echo "FAIL: $want did not pass" >&2
		status=1
	fi
done
rm -f "$out"
if [ "$status" -ne 0 ]; then
	echo "FAIL: acceptance-s3" >&2
	exit "$status"
fi
echo "PASS: acceptance-s3"
