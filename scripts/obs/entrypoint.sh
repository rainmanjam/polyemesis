#!/usr/bin/env bash
# Runs OBS headless and makes it publish. See the Dockerfile for why this is
# harder than "run obs".
set -euo pipefail

# The bounded stop at the foot of this file. A separate file because a container
# entrypoint cannot be unit-tested and that function can be -- see #208, where a
# blind edit to these last two lines was the thing being avoided.
# shellcheck source=scripts/obs/lib-stop.sh
. "$(dirname "$0")/lib-stop.sh"

RTMP_URL="${RTMP_URL:-rtmp://host.docker.internal:1935/live}"
RTMP_KEY="${RTMP_KEY:-}"
TRACKS="${TRACKS:-3}"
SECONDS_TO_STREAM="${SECONDS_TO_STREAM:-25}"
PROFILE=poly
COLLECTION=poly

log() { printf '  [obs] %s\n' "$*" >&2; }

# ---- a display that is attached to nothing -------------------------------
Xvfb :99 -screen 0 1280x720x24 -nolisten tcp &
for _ in $(seq 1 50); do xdpyinfo -display :99 >/dev/null 2>&1 && break; sleep 0.2; done
xdpyinfo -display :99 >/dev/null 2>&1 || { log "Xvfb never came up"; exit 1; }
log "Xvfb up on :99"

# ---- an audio server, because OBS refuses to start without one -----------
# A null sink: nothing is captured from it, the tones come from media sources.
pulseaudio -D --exit-idle-time=-1 --disallow-exit >/dev/null 2>&1 || true
sleep 1
pactl load-module module-null-sink sink_name=dummy >/dev/null 2>&1 || true

# ---- the tones, one per track, distinguishable on arrival ----------------
# Same frequencies as scripts/verify_ertmp_multitrack.go, so a failure here can
# be compared against a failure there directly.
FREQS=(300 500 700 1100 1300 1700)
mkdir -p /media
for i in $(seq 0 $((TRACKS - 1))); do
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "sine=frequency=${FREQS[$i]}:duration=600" \
    -c:a aac -b:a 128k "/media/tone$i.m4a"
done
log "built $TRACKS tone files"

# ---- OBS configuration ---------------------------------------------------
# A bitmask over tracks 1..TRACKS: three tracks is 0b111.
MIXES=$(( (1 << TRACKS) - 1 ))

CFG="$HOME/.config/obs-studio"
mkdir -p "$CFG/basic/profiles/$PROFILE" "$CFG/basic/scenes"

cat > "$CFG/global.ini" <<INI
[General]
FirstRun=true
LastVersion=503316480
ConfirmOnExit=false

[Basic]
Profile=$PROFILE
ProfileDir=$PROFILE
SceneCollection=$COLLECTION
SceneCollectionFile=$COLLECTION
INI

cat > "$CFG/basic/profiles/$PROFILE/service.json" <<JSON
{
  "type": "rtmp_custom",
  "settings": {
    "server": "$RTMP_URL",
    "key": "$RTMP_KEY",
    "use_auth": false
  }
}
JSON

# TrackIndex is the legacy single-track selector; the multitrack bitmask is
# written by the caller-visible knob below and confirmed after the first run.
cat > "$CFG/basic/profiles/$PROFILE/basic.ini" <<INI
[Output]
Mode=Advanced

[AdvOut]
TrackIndex=1
# THE MULTITRACK KNOB. A bitmask of the audio tracks the STREAM carries, as
# distinct from TrackIndex, which selects exactly one and is what OBS falls back
# to. Found by reading the frontend binary's string table: the UI calls these
# advOutMultiTrack1..6 and persists them here.
#
# Without it OBS publishes a single track and this whole suite passes while
# measuring nothing — which is exactly what the first run did, reporting one
# arriving track against three sent.
StreamMultiTrackAudioMixes=$MIXES
Encoder=obs_x264
RecType=Standard
FFOutputToFile=true
Track1Bitrate=128
Track2Bitrate=128
Track3Bitrate=128
Track4Bitrate=128
Track5Bitrate=128
Track6Bitrate=128

[Video]
BaseCX=1280
BaseCY=720
OutputCX=1280
OutputCY=720
FPSCommon=30

[Audio]
SampleRate=48000
ChannelSetup=Stereo
INI

# ---- the scene: one media source per track ------------------------------
# "mixers" is a bitmask of the audio tracks a source feeds: 1<<0 is track 1.
# One source per track means each arriving track carries exactly one tone, so a
# reordering is visible rather than smeared across a mix.
#
# The sources must ALSO appear as items of the scene. A source that nothing
# references is unreferenced, and OBS destroys it on load — the first version of
# this file declared the sources and left settings.items empty, and the log said
# exactly that: "source 'tone0' destroyed", three times, then a scene with
# nothing in it and silence on the wire.
build_scene() {
  local n="$1" i uuid
  local sources="[]" items="[]"
  for i in $(seq 0 $((n - 1))); do
    uuid="00000000-0000-0000-0000-00000000000$i"
    sources=$(jq --argjson m "$((1 << i))" --arg name "tone$i" --arg uuid "$uuid" \
      --arg file "/media/tone$i.m4a" '. += [{
        prev_ver: 503316480, name: $name, uuid: $uuid,
        id: "ffmpeg_source", versioned_id: "ffmpeg_source",
        settings: { local_file: $file, looping: true, is_local_file: true },
        mixers: $m, sync: 0, flags: 0, volume: 1.0, balance: 0.5,
        enabled: true, muted: false, push_to_mute: false, push_to_talk: false,
        monitoring_type: 0, private_settings: {}, hotkeys: {}
      }]' <<< "$sources")
    items=$(jq --arg name "tone$i" --arg uuid "$uuid" --argjson id "$((i + 1))" \
      '. += [{
        name: $name, source_uuid: $uuid, visible: true, locked: false,
        rot: 0.0, scale_ref: { x: 1.0, y: 1.0 },
        pos: { x: 0.0, y: 0.0 }, scale: { x: 1.0, y: 1.0 },
        align: 5, bounds_type: 0, bounds_align: 0,
        bounds: { x: 0.0, y: 0.0 }, crop_left: 0, crop_top: 0,
        crop_right: 0, crop_bottom: 0, id: $id, group_item_backup: false,
        scale_filter: "disable", blend_method: "default", blend_type: "normal",
        show_transition: { duration: 0 }, hide_transition: { duration: 0 },
        private_settings: {}
      }]' <<< "$items")
  done

  jq -n --argjson sources "$sources" --argjson items "$items" --arg coll "$COLLECTION" '{
    current_scene: "Scene",
    current_program_scene: "Scene",
    scene_order: [{ name: "Scene" }],
    name: $coll,
    sources: ($sources + [{
      prev_ver: 503316480, name: "Scene",
      uuid: "00000000-0000-0000-0000-0000000000ff",
      id: "scene", versioned_id: "scene",
      settings: { custom_size: false, id_counter: 99, items: $items },
      mixers: 0, sync: 0, flags: 0, volume: 1.0, balance: 0.5,
      enabled: true, muted: false, monitoring_type: 0,
      private_settings: {}, hotkeys: {}
    }])
  }'
}
build_scene "$TRACKS" > "$CFG/basic/scenes/$COLLECTION.json"
log "configured: $TRACKS track(s) -> $RTMP_URL"

if [ "${1:-}" = "shell" ]; then exec /bin/bash; fi

# ---- go ------------------------------------------------------------------
# PROCESS SUBSTITUTION, NOT A PIPELINE, and the difference is the whole of #208.
#
# This used to be `obs ... 2>&1 | sed 's/^/  [obs] /' &` followed by
# `OBS_PID=$!`. After a pipeline `$!` is the LAST element, so OBS_PID named the
# log prefixer. The `kill -TERM` below killed sed, OBS carried on with its stdout
# closed, and the `wait` returned the instant sed was reaped -- which is why this
# never hung and also never stopped OBS.
#
# `> >(sed ...) 2>&1 &` backgrounds obs ITSELF, so `$!` is obs. The `[obs]`
# prefix on every line is unchanged; only which process the shell is holding is.
obs --startstreaming --minimize-to-tray --disable-updater --verbose \
	> >(sed 's/^/  [obs] /') 2>&1 &
OBS_PID=$!
sleep "$SECONDS_TO_STREAM"
log "stopping"

# BOUNDED, and it re-observes. `kill -TERM` then an unbounded `wait` is #179's
# body, and it is latent here only for as long as the signal goes to the wrong
# process; the line above just fixed that. poly_bounded_stop asks, watches for
# 20s, escalates to SIGKILL, watches again, and reports loudly rather than
# blocking for ever. 20s because OBS finalises its RTMP session and flushes its
# encoders on the way out and this container has no GPU, so the shutdown is
# software-encoded; the ceiling is a blast radius, not a fitted measurement.
if ! poly_bounded_stop "$OBS_PID" obs 20 5; then
	log "OBS outlived SIGKILL; the container runner is now the only thing that will reclaim it"
fi

# The prefixer drains what OBS wrote on its way out. This sleep is AFTER the
# death has been observed, so it is a flush and not a wait on a death that may
# never come -- which is the distinction the rest of this file is about.
sleep 1
