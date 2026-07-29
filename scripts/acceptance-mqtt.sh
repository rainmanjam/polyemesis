#!/usr/bin/env bash
# Retained MQTT telemetry, against a real broker.
#
# The unit tests assert the topic strings, the retain bit and the QoS against a
# fake. They cannot assert the one thing that matters most, because it is a
# property of the BROKER rather than of our code: that a subscriber which was
# not connected when the state changed still receives it.
#
# So every check here connects a FRESH subscriber after the fact. Two of them
# are the reason the suite exists:
#
#   * the will message -- polyemesis is SIGKILLed, and a subscriber that
#     connects afterwards is told it is offline. Nothing in our process runs on
#     that path; the broker does it on our behalf, from a promise made at
#     connect time.
#   * broker restart -- the broker is stopped and started, and the telemetry
#     comes back. This is what QoS 1 retained buys and what QoS 0 would fail
#     silently, because a conforming broker MAY decline to store a retained
#     QoS 0 message.
#
# Usage:  ./scripts/acceptance-mqtt.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-mqtt}"
PORT=8099
BROKER_PORT=18830
INSTANCE="acceptance"
PREFIX="polyemesis"
BROKER_URL="mqtt://127.0.0.1:$BROKER_PORT"
CONTAINER="polyemesis-acceptance-mosquitto"
# A destination named so that it MUST be slugged, and so that a collision with
# its sibling below is possible if the hash is ever dropped.
DEST_A='Twitch (main)'
DEST_B='Twitch [main]'
# Planted in the destination's stream key by the driver. If this string ever
# appears on a topic, the whitelist has been breached.
SECRET='acceptance-stream-key-do-not-publish'

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPTS/lib-cleanup.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"
DRIVER="$ROOT/scripts/acceptance_mqtt_driver.go"
API="http://127.0.0.1:$PORT"

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
note() { printf "  \033[36mNOTE\033[0m  %s\n" "$1"; }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  poly_cleanup "$PORT" "${WORK:-}"
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v go >/dev/null || { echo "go is required to run the driver"; exit 1; }
command -v docker >/dev/null || { echo "docker is required: the broker runs as a container so no host install is needed"; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"

# Run the driver from the repository root, not from $WORK.
#
# Every other suite's driver is stdlib-only, so `go run` worked from any
# directory. This one imports paho to act as an independent subscriber, and `go
# run` resolves imports against the CWD's module -- which under $WORK is no
# module at all. The failure is a compile error per invocation, which the shell
# reports as "could not create the destination" and sends you looking in
# entirely the wrong place.
drive() { (cd "$ROOT" && go run "$DRIVER" "$@"); }

# dump prints `topic<TAB>payload` for everything a NEW subscriber receives.
snapshot() { drive "$API" dump "$BROKER_URL" "$PREFIX" 2>/dev/null; }
# payload_of reads one topic out of a dump. Exact match on the topic column, so
# a prefix of another topic cannot be mistaken for it.
payload_of() { awk -F'\t' -v t="$2" '$1==t {print $2}' <<<"$1"; }

start_broker() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  # allow_anonymous because the suite is testing telemetry, not authentication;
  # the password path is covered by the sealed-storage unit test.
  docker run -d --name "$CONTAINER" -p "$BROKER_PORT:1883" \
    eclipse-mosquitto:2 \
    sh -c 'printf "listener 1883\nallow_anonymous true\npersistence false\n" > /m.conf && exec mosquitto -c /m.conf' \
    >/dev/null 2>&1
}

step "1. A broker, and a subscriber that can reach it"
start_broker || true
for _ in $(seq 1 40); do
  sleep 0.5
  docker logs "$CONTAINER" 2>&1 | grep -q "running" && break
done
if docker ps --format '{{.Names}}' | grep -q "^$CONTAINER$"; then
  ok "mosquitto is listening on $BROKER_PORT"
else
  bad "the broker container did not start"; exit 1
fi

step "2. Server up, MQTT off"
"$BIN" -addr ":$PORT" -data ./data -log warn > server.log 2>&1 &
for _ in $(seq 1 40); do sleep 0.3; grep -q "web ui" server.log 2>/dev/null && break; done
sleep 1
if grep -q "polyemesis" server.log; then
  ok "server started"
else
  bad "server did not start"; exit 1
fi
drive "$API" setup >/dev/null 2>&1 || true

# Nothing must be on the broker before it is switched on. A suite that never
# checked this would pass against a build that published unconditionally.
before="$(snapshot)"
if [ -z "$before" ]; then
  ok "nothing is published while MQTT is disabled"
else
  bad "topics exist before MQTT was enabled: $(wc -l <<<"$before") of them"
fi

step "3. Two destinations whose names collide when slugged"
# Both in one check, and only on success. An unconditional `ok` after two
# fallible commands reports a pass having done nothing -- which is exactly the
# shape the fixed-value check-count guard at the bottom exists to catch, and it
# was in this file once.
if drive "$API" dest "$DEST_A" >/dev/null && drive "$API" dest "$DEST_B" >/dev/null; then
  ok "created '$DEST_A' and '$DEST_B'"
else
  bad "could not create the two destinations"
fi

step "4. Switch MQTT on"
drive "$API" configure "$BROKER_URL" "$INSTANCE" >/dev/null || bad "could not enable MQTT"
# The runner polls settings every 5s, then publishes on a 1s tick.
sleep 9
dump1="$(snapshot)"
count1=$(printf '%s' "$dump1" | grep -c . )
if [ "$count1" -gt 0 ]; then
  ok "a subscriber that connected AFTER the fact received $count1 retained topics"
else
  bad "a fresh subscriber received nothing; retained delivery is not working"
fi

step "5. Availability, host and source state"
status="$(payload_of "$dump1" "$PREFIX/$INSTANCE/status")"
if [ "$status" = "online" ]; then
  ok "the status topic reads 'online'"
else
  bad "the status topic reads '${status:-<absent>}', want 'online'"
fi
host="$(payload_of "$dump1" "$PREFIX/$INSTANCE/state")"
if grep -q '"destinations":2' <<<"$host"; then
  ok "host state counts both destinations"
else
  bad "host state does not count 2 destinations: ${host:-<absent>}"
fi
src_topics=$(awk -F'\t' -v p="$PREFIX/$INSTANCE/source/" 'index($1,p)==1 && $1 ~ /\/state$/ && $1 !~ /\/dest\/|\/rendition\// {n++} END{print n+0}' <<<"$dump1")
if [ "$src_topics" -eq 1 ]; then
  ok "exactly one source state topic"
else
  bad "$src_topics source state topics, want 1"
fi

step "6. The slug collision, on a real broker"
dest_topics=$(awk -F'\t' '$1 ~ /\/dest\/.*\/state$/ {print $1}' <<<"$dump1")
n_dest=$(printf '%s' "$dest_topics" | grep -c .)
if [ "$n_dest" -eq 2 ]; then
  ok "'$DEST_A' and '$DEST_B' occupy two distinct topics"
else
  bad "$n_dest destination topics, want 2 -- the slug hash is not separating them"
  printf '%s\n' "$dest_topics" | sed 's/^/        /'
fi

step "7. No credential reaches any topic"
# The destination's stream key is a string nothing in the payload whitelist can
# carry. Grepping the whole dump is the check that would catch a struct which
# grew a field.
#
# Gated on the dump being non-empty, because "no secret found" is exactly what
# an empty dump reports. A leak check that passes hardest when nothing is
# published at all is worse than no check: it is a green light pointing the
# wrong way.
if [ "$count1" -eq 0 ]; then
  bad "no payloads to search -- the leak check cannot run and must not report a pass"
  bad "no payloads to search for credential-shaped field names either"
elif grep -q "$SECRET" <<<"$dump1"; then
  bad "the destination's stream key appears on a topic"
  grep -n "$SECRET" <<<"$dump1" | head -3 | sed 's/^/        /'
else
  ok "the stream key appears on no topic"
fi
if [ "$count1" -eq 0 ]; then
  : # already counted as a failure above
elif grep -qiE '"(url|streamKey|token|password|passphrase)"' <<<"$dump1"; then
  bad "a credential-shaped field name appears in a payload"
else
  ok "no credential-shaped field name appears in any payload"
fi

step "8. Home Assistant discovery"
disc="$(payload_of "$dump1" "homeassistant/device/$INSTANCE/config")"
if [ -n "$disc" ]; then
  ok "the discovery payload is retained and reaches a fresh subscriber"
else
  bad "no discovery payload"
fi
if grep -q "\"avty_t\":\"$PREFIX/$INSTANCE/status\"" <<<"$disc"; then
  ok "every entity's availability is wired to the will topic"
else
  bad "discovery does not point availability at the status topic"
fi

step "9. Deleting a destination clears its retained topic"
# Risk 1 from the design: a retained message outlives the process that sent it,
# so a deleted destination would keep reporting the last state it ever saw,
# forever, with a Home Assistant entity still attached to it.
deleted_name="$(drive "$API" rmfirst)"
if [ -z "$deleted_name" ]; then
  bad "could not delete a destination"
else
  ok "deleted '$deleted_name' through the API the UI uses"
fi
# Its topic is the one carrying that destination's name in the payload.
gone_topic=$(awk -F'\t' -v n="\"name\":\"$deleted_name\"" '$1 ~ /\/dest\/.*\/state$/ && index($2,n) {print $1; exit}' <<<"$dump1")
sleep 4
dump2="$(snapshot)"
if [ -z "$gone_topic" ]; then
  bad "could not identify the deleted destination's topic in the first dump"
elif [ -z "$(payload_of "$dump2" "$gone_topic")" ]; then
  ok "its retained topic was cleared -- a fresh subscriber no longer sees it"
else
  bad "the deleted destination still reports on $gone_topic"
fi
# The sibling must survive. Without this, a sweep that cleared everything would
# pass the check above.
if [ "$(awk -F'\t' '$1 ~ /\/dest\/.*\/state$/ && $2 != "" {n++} END{print n+0}' <<<"$dump2")" -eq 1 ]; then
  ok "the other destination is untouched"
else
  bad "the sweep did not leave exactly one destination behind"
fi

step "10. The broker restarts and the telemetry comes back"
# This is what QoS 1 retained buys. A conforming broker MAY decline to store a
# retained QoS 0 message, so at QoS 0 this check is what would fail -- silently,
# and only here.
docker restart "$CONTAINER" >/dev/null 2>&1
sleep 3
for _ in $(seq 1 20); do sleep 1; docker logs "$CONTAINER" 2>&1 | tail -5 | grep -q "running" && break; done
sleep 8
dump3="$(snapshot)"
status3="$(payload_of "$dump3" "$PREFIX/$INSTANCE/status")"
host3="$(payload_of "$dump3" "$PREFIX/$INSTANCE/state")"
if [ "$status3" = "online" ]; then
  ok "after a broker restart the status topic reads 'online' again"
else
  bad "after a broker restart the status topic reads '${status3:-<absent>}'"
fi
if [ -n "$host3" ]; then
  ok "host state was republished after the broker lost its retained set"
else
  bad "host state did not come back after the broker restart"
fi

step "11. The will message: polyemesis is killed, not stopped"
# SIGKILL, deliberately. A clean shutdown publishes 'offline' itself, which
# would test our code rather than the broker's promise. This is the case the
# whole design exists for: nothing of ours runs here.
pid=$(pgrep -f "polyemesis -addr :$PORT" | head -1)
if [ -n "$pid" ]; then
  kill -9 "$pid" 2>/dev/null
  ok "polyemesis was SIGKILLed with no chance to say goodbye"
else
  bad "could not find the server process to kill"
fi
# Wait past the 5s keep-alive: the broker only fires the will once it decides
# the connection is dead.
sleep 14
dump4="$(snapshot)"
status4="$(payload_of "$dump4" "$PREFIX/$INSTANCE/status")"
if [ "$status4" = "offline" ]; then
  ok "a subscriber connecting after the crash is told the instance is offline"
else
  bad "the status topic reads '${status4:-<absent>}' after a crash, want 'offline'"
  note "this is the will message. Without it a dead instance's dashboard keeps"
  note "showing its last reading indefinitely -- confidently wrong."
fi

step "Summary"
total=$((pass + fail))
printf "  %d passed, %d failed\n" "$pass" "$fail"

# Fixed-value guard. A suite whose checks live behind conditionals can report
# "N passed, 0 failed" having silently skipped half of them.
EXPECTED_CHECKS=20
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  %d of %d checks ran; the rest never executed.\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
[ "$fail" -eq 0 ] || exit 1
printf "\n  \033[32mRetained telemetry survives a broker restart and reports a crash.\033[0m\n\n"
