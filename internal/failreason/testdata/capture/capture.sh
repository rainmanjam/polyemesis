#!/usr/bin/env bash
# Re-captures the FFmpeg stderr fixtures in ../ from real FFmpeg against local
# ingest servers. See ../README.md for what each scenario is and why.
#
# Everything this starts is named pe-w1-* and everything it removes is named
# pe-w1-*. It never prunes and never touches another container, network or
# image. No public endpoint is contacted: every target is a container on a
# private bridge network, or a name under .invalid, which RFC 6761 guarantees
# never resolves.
#
# Usage: internal/failreason/testdata/capture/capture.sh
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out="$(cd "$here/.." && pwd)"
net=pe-w1-net
img=pe-w1-ffmpeg:8.1.3

cleanup() {
	for c in pe-w1-nginx pe-w1-mediamtx pe-w1-blackhole; do
		docker rm -f "$c" >/dev/null 2>&1 || true
	done
	docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker build -q -t "$img" -f "$here/Dockerfile.ffmpeg" "$here" >/dev/null
docker network create "$net" >/dev/null

# A self-signed certificate for MediaMTX's RTMPS listener, generated fresh each
# run inside the FFmpeg image so the host needs no openssl of its own.
certs="$(mktemp -d)"
docker run --rm -v "$certs:/certs" "$img" sh -c \
	'openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=pe-w1-mediamtx \
	  -keyout /certs/server.key -out /certs/server.crt 2>/dev/null && chmod 644 /certs/*'

docker run -d --name pe-w1-nginx --network "$net" \
	-v "$here/nginx.conf:/etc/nginx/nginx.conf:ro" tiangolo/nginx-rtmp:latest >/dev/null
docker run -d --name pe-w1-mediamtx --network "$net" \
	-v "$here/mediamtx.yml:/mediamtx.yml:ro" -v "$certs:/certs:ro" bluenviron/mediamtx:1 >/dev/null
# Accepts the TCP connection and then says nothing, for ever: an ingest behind a
# load balancer whose backend has gone. Only -rw_timeout ends this.
docker run -d --name pe-w1-blackhole --network "$net" "$img" \
	sh -c 'while true; do sleep 3600 | nc -l -p 1935 >/dev/null; done' >/dev/null
sleep 3

# One source for every scenario: what a passthrough destination carries.
src=(-re -f lavfi -i "testsrc2=size=640x360:rate=30" -f lavfi -i "sine=frequency=440:sample_rate=48000")
enc=(-c:v libx264 -preset ultrafast -tune zerolatency -g 60 -pix_fmt yuv420p -c:a aac -b:a 128k)

manifest="$out/manifest.tsv"
printf 'fixture\texit\tran_ms\tcommand\n' >"$manifest"

# capture NAME FFMPEG-OUTPUT-ARGS...
#
# Runs FFmpeg exactly as a destination does -- -loglevel warning, -nostats,
# -progress on stdout -- for at most 20 s, and keeps stderr and the wall time.
#
# Two knobs, both empty by default: $pre is a shell command run in the FFmpeg
# container first (the throttle uses it, which is why $docker_extra can grant
# NET_ADMIN), and $keep_progress keeps the -progress stream as NAME.progress
# for the one scenario whose cause is a timing rather than a sentence.
pre=""
docker_extra=()
keep_progress=""
capture() {
	local name=$1
	shift
	local start end code progress=/dev/null
	[ -n "$keep_progress" ] && progress="$out/$name.progress"
	start=$(python3 -c 'import time;print(time.time_ns())')
	set +e
	docker run --rm --name "pe-w1-ff-$name" --network "$net" ${docker_extra[@]+"${docker_extra[@]}"} "$img" \
		sh -c "$pre"' timeout 20 ffmpeg -hide_banner -loglevel warning -nostats -progress pipe:1 "$@"' \
		sh "${src[@]}" "$@" \
		2>"$out/$name.stderr" >"$progress"
	code=$?
	set -e
	end=$(python3 -c 'import time;print(time.time_ns())')
	printf '%s\t%s\t%s\tffmpeg %s\n' "$name" "$code" $(((end - start) / 1000000)) "$*" >>"$manifest"
	echo "$name: exit $code"
}

# Refused at publish: the key, as the ingest sees it.
capture rtmp-onpublish-403 "${enc[@]}" -t 15 -f flv rtmp://pe-w1-nginx/live/badkey
capture rtmp-auth-failed "${enc[@]}" -t 15 -f flv "rtmp://pe-w1-mediamtx/secured?user=publisher&pass=wrongpass"
capture srt-wrong-passphrase "${enc[@]}" -t 15 -f mpegts "srt://pe-w1-mediamtx:8890?streamid=publish:secured:publisher:wrongpass"
capture icecast-401 -vn -c:a libmp3lame -b:a 128k -t 15 -content_type audio/mpeg -f mp3 icecast://source:wrongpass@pe-w1-nginx:8000/live.mp3
# Refused at publish for a reason that is NOT the key, and that FFmpeg prints
# identically to one that is. Kept so the README can say so with evidence.
capture rtmp-unknown-app "${enc[@]}" -t 15 -f flv rtmp://pe-w1-nginx/noapp/goodkey
capture rtmp-unconfigured-path "${enc[@]}" -t 15 -f flv rtmp://pe-w1-mediamtx/notconfigured
capture srt-unconfigured-path "${enc[@]}" -t 15 -f mpegts "srt://pe-w1-mediamtx:8890?streamid=publish:notconfigured"
# Up, and not taking this stream yet.
capture icecast-503 -vn -c:a libmp3lame -b:a 128k -t 15 -content_type audio/mpeg -f mp3 icecast://source:goodpass@pe-w1-nginx:8001/live.mp3
docker run -d --name pe-w1-ff-holder --network "$net" "$img" \
	timeout 30 ffmpeg -hide_banner -loglevel quiet "${src[@]}" "${enc[@]}" -f flv rtmp://pe-w1-nginx/live/goodheld >/dev/null
sleep 3
capture rtmp-already-publishing "${enc[@]}" -t 15 -f flv rtmp://pe-w1-nginx/live/goodheld
docker rm -f pe-w1-ff-holder >/dev/null 2>&1 || true
# The network.
capture rtmp-dns-unresolvable "${enc[@]}" -t 15 -f flv rtmp://pe-w1-no-such-ingest.invalid/live/goodkey
capture rtmp-connection-refused "${enc[@]}" -t 15 -f flv rtmp://pe-w1-nginx:1936/live/goodkey
capture rtmp-silent-ingest-timeout "${enc[@]}" -t 15 -rw_timeout 5000000 -f flv rtmp://pe-w1-blackhole:1935/live/goodkey
# TLS.
capture rtmps-to-plain-port "${enc[@]}" -t 15 -f flv rtmps://pe-w1-nginx:1935/live/goodkey
capture rtmps-untrusted-cert "${enc[@]}" -t 15 -tls_verify 1 -f flv rtmps://pe-w1-mediamtx:1936/open
# Codecs: refused by FFmpeg's own muxer, and dropped silently by the ingest.
capture flv-mpeg2-refused -c:v mpeg2video -c:a aac -t 15 -f flv rtmp://pe-w1-nginx/live/goodkey
capture rtmp-flv1-dropped -c:v flv1 -c:a aac -t 15 -f flv rtmp://pe-w1-mediamtx/open
# Upload too slow: egress capped at 16 kbit/s under a ~1 Mbit/s stream. Media
# moves at first -- the socket buffer takes it -- then speed collapses and the
# muxer blocks on the socket until -rw_timeout or the 20 s cap ends it. The cause is in the
# timing, so the -progress stream is kept beside stderr.
pre="tc qdisc add dev eth0 root tbf rate 16kbit burst 2kb latency 400ms &&"
docker_extra=(--cap-add NET_ADMIN)
keep_progress=1
capture rtmp-throttled-upload "${enc[@]}" -b:v 900k -t 18 -rw_timeout 5000000 -f flv rtmp://pe-w1-nginx/live/goodslow
pre=""
docker_extra=()
keep_progress=""
# The control: a key the ingest accepts, so a classifier that calls everything
# a failure is caught.
capture rtmp-accepted "${enc[@]}" -t 4 -f flv rtmp://pe-w1-nginx/live/goodkey

rm -rf "$certs"
