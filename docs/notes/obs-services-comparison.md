# What OBS's service registry says, and what polyemesis should take from it

Source: `obsproject/obs-studio` → `plugins/rtmp-services/data/services.json`
(schema v5, 540 server URLs across ~200 services). Fetched 2026-08-13.

## The finding that matters

**529 of 540 server URLs carry an app path.** Only 11 do not.

An RTMP publish URL without an app path is the anomaly, not the norm. Kick's
dashboard hands you exactly that anomaly:

    rtmps://fa723fc1b171.global-contribute.live-video.net/

Pasted verbatim, polyemesis composed `rtmps://<host>/<key>` — the stream key
became the *app name* and there was no stream name at all. Amazon IVS, which
Kick runs on, refuses it. Symptom: `state=reconnecting`, `outTimeMs=0`, the far
end dropping the connection on every retry.

Correct form: `rtmps://<host>:443/app` + key → `rtmps://<host>:443/app/<key>`.

With that one change the multistream suite went from 42/2 to **44 passed, 0
failed**, Kick delivering 75.7s of media.

NOT ISOLATED: the fix changed two things at once, `:443` and `/app`. 88 of OBS's
rtmps URLs omit an explicit port, so FFmpeg clearly defaults it and `/app` is
almost certainly the operative change — but this was not proven by separate
runs and should not be written down as if it were.

## Kick is absent from services.json

Deliberately. Kick issues a per-channel IVS ingest host, so there is no static
entry OBS could ship; Kick users pick "Custom..." and paste. That is also why
`KICK_INGEST_URL` has no default in the acceptance suite.

The consequence: Kick is the one platform where the operator supplies the whole
URL by hand, and therefore the one where a malformed URL is most likely.

## The four platforms, as OBS has them

| | OBS server URL | polyemesis default |
|---|---|---|
| Twitch | `rtmp://live-<region>.twitch.tv/app` (46 regions) | `rtmp://live.twitch.tv/app` |
| YouTube | `rtmps://a.rtmps.youtube.com:443/live2` | `rtmp://a.rtmp.youtube.com/live2` (OBS's legacy entry) |
| Facebook | `rtmps://rtmp-api.facebook.com:443/rtmp/` | `rtmps://live-api-s.facebook.com:443/rtmp` |
| Kick | *(absent — per-channel)* | none; required |

All four of ours are valid; YouTube's is OBS's legacy RTMP entry rather than the
RTMPS primary.

## What OBS encodes that we do not

`recommended` blocks, per service:

- `keyint` — universally 2 seconds
- `max video bitrate` — Twitch 6000, YouTube 51000, Facebook 9000
- `max audio bitrate` — Twitch 320, YouTube 160, Facebook 128
- `x264opts: scenecut=0` — Twitch
- `supported resolutions`, `max fps`, and for Facebook a full `bitrate matrix`
  of res × fps → max bitrate
- `supported video codecs` / `supported audio codecs`

## Proposed, in order of value

1. **Reject or warn on a destination URL with no app path.** This is the defect
   above, it is cheap, and 529/540 says the heuristic is sound. A bare
   `rtmps://host/` should not silently become a publish target.
2. **Adopt the `recommended` ceilings as validation** at destination-create
   time — an audio bitrate above YouTube's 160 or Facebook's 128 is a
   misconfiguration the operator cannot see until the platform complains.
3. **A service registry of our own**, seeded from OBS's, so the operator picks
   "Twitch" rather than typing a URL. Largest change; removes the whole class.
