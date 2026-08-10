#!/usr/bin/env bash
# Runs OBS headless and makes it publish. See the Dockerfile for why this is
# harder than "run obs".
set -euo pipefail

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
obs --startstreaming --minimize-to-tray --disable-updater --verbose 2>&1 | sed 's/^/  [obs] /' &
OBS_PID=$!
sleep "$SECONDS_TO_STREAM"
log "stopping"
kill -TERM "$OBS_PID" 2>/dev/null || true
wait "$OBS_PID" 2>/dev/null || true
