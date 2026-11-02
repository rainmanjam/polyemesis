#!/usr/bin/env bash
# S3 offload against a real S3-compatible server: RustFS, in a container.
#
# Starts RustFS, waits until its S3 API answers, then runs the
# s3integration-tagged Go tests in internal/objstore (SigV4 round trip, the
# server refusing a body that is not the signed digest, a wrong secret refused
# and never echoed, multipart with its composite ETag, abort) and
# internal/offload (mirror mode: upload and verify, the local file untouched;
# a multipart upload resumed across a restart; the Test button's
# put/get/delete). Unit CI
# never needs Docker: those files only build with -tags s3integration, and
# they FAIL rather than skip without an endpoint, so a run that reached the
# server is the only green this prints.
#
# The tests are not RustFS-specific. Point them at any S3-compatible server by
# exporting POLYEMESIS_S3_ENDPOINT, _ACCESS_KEY, _SECRET_KEY and optionally
# _BUCKET, _REGION, _PATH_STYLE=0 and _CREATE_BUCKET=1, then run the go test
# line below yourself; or set POLYEMESIS_S3_ENDPOINT before running this
# script and it uses that server instead of starting RustFS.
#
# The image is RustFS 1.0.4 pinned by digest (the same digest is published as
# docker.io/rustfs/rustfs:1.0.4 and quay.io/rustfs/rustfs:1.0.4; set
# POLYEMESIS_RUSTFS_REGISTRY=quay.io to pull the mirror). Its S3 API listens on
# 9000 inside the container (the console is 9001).
#
# LOCAL ONLY, and required before merging offload changes. CI never builds
# these tests (the s3integration tag keeps them out of `go test ./...`) and no
# CI job or required check runs this script; docs/TESTING.md says when to run
# it and CONTRIBUTING.md asks for its output in the pull request.
#
# Usage:  ./scripts/acceptance-s3.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIGEST="sha256:ed9210d6411d77ffceb70d94b964a3cb7d225983368bc25bebe01e995293dcc2" # rustfs 1.0.4
REGISTRY="${POLYEMESIS_RUSTFS_REGISTRY:-docker.io}"
IMAGE="$REGISTRY/rustfs/rustfs@$DIGEST"
CONTAINER="polyemesis-acceptance-rustfs"
PORT="${POLYEMESIS_S3_PORT:-19000}"
# Obviously fake, low-entropy credentials for a throwaway local container.
ACCESS="polytestuser"
SECRET="polytestpassword"

command -v go >/dev/null 2>&1 || { echo "FAIL: go is required" >&2; exit 1; }

if [ -n "${POLYEMESIS_S3_ENDPOINT:-}" ]; then
	echo "== using the S3 server at $POLYEMESIS_S3_ENDPOINT"
	ENDPOINT="$POLYEMESIS_S3_ENDPOINT"
	ACCESS="${POLYEMESIS_S3_ACCESS_KEY:?set POLYEMESIS_S3_ACCESS_KEY with POLYEMESIS_S3_ENDPOINT}"
	SECRET="${POLYEMESIS_S3_SECRET_KEY:?set POLYEMESIS_S3_SECRET_KEY with POLYEMESIS_S3_ENDPOINT}"
else
	command -v docker >/dev/null 2>&1 || { echo "FAIL: docker is required (RustFS runs as a container)" >&2; exit 1; }
	command -v curl >/dev/null 2>&1 || { echo "FAIL: curl is required to wait for RustFS" >&2; exit 1; }
	cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
	trap cleanup EXIT
	cleanup

	echo "== starting RustFS ($IMAGE) on 127.0.0.1:$PORT"
	docker run -d --name "$CONTAINER" -p "127.0.0.1:$PORT:9000" \
		-e RUSTFS_ACCESS_KEY="$ACCESS" -e RUSTFS_SECRET_KEY="$SECRET" \
		-e RUSTFS_VOLUMES=/data \
		"$IMAGE" >/dev/null
	ENDPOINT="http://127.0.0.1:$PORT"

	# READY means the S3 API answers an HTTP request -- any status, an
	# anonymous ListBuckets is refused with 403 -- not that the container
	# started: RustFS formats its volume before it binds, and its stdout says
	# nothing about either.
	ready=""
	for _ in $(seq 1 120); do
		code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$ENDPOINT/" || true)"
		if [ -n "$code" ] && [ "$code" != "000" ]; then
			ready=1
			break
		fi
		if [ "$(docker inspect -f '{{.State.Status}}' "$CONTAINER" 2>/dev/null || echo gone)" != running ]; then
			break
		fi
		sleep 0.5
	done
	if [ -z "$ready" ]; then
		echo "FAIL: RustFS did not answer on $ENDPOINT; its log follows" >&2
		docker logs "$CONTAINER" >&2 || true
		docker exec "$CONTAINER" sh -c 'tail -50 /logs/*' >&2 || true
		exit 1
	fi
fi

echo "== go test -tags s3integration"
cd "$ROOT"
out="$(mktemp)"
status=0
POLYEMESIS_S3_ENDPOINT="$ENDPOINT" \
	POLYEMESIS_S3_ACCESS_KEY="$ACCESS" POLYEMESIS_S3_SECRET_KEY="$SECRET" \
	POLYEMESIS_S3_CREATE_BUCKET="${POLYEMESIS_S3_CREATE_BUCKET:-1}" \
	go test -race -count=1 -tags s3integration -run 'Live' -v -timeout 5m \
	./internal/objstore/ ./internal/offload/ >"$out" 2>&1 || status=$?
grep -E '^(=== RUN|--- |ok|FAIL|panic)' "$out" || true

# A mistyped -run exits 0 having run nothing; insist on the cases by name.
for want in TestLiveRoundTripWithAKeyThatNeedsEscaping TestLiveServerRefusesABodyThatIsNotTheSignedDigest \
	TestLiveWrongSecretIsRefusedAndNeverEchoed TestLiveMirrorUploadsVerifiesAndKeepsTheLocalFile \
	TestLiveProbeRunsPutGetDelete TestLiveMultipartCompleteAndAbort \
	TestLiveMultipartResumesAcrossARestartAndVerifies; do
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
