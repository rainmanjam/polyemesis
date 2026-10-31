# polyemesis Stream Deck plugin

An Elgato Stream Deck plugin for a polyemesis install. It shows what each
destination is really doing, starts and stops destinations, mutes and sets the
gain of one audio track on a destination, and shows the audio meters on a
Stream Deck+ dial.

It talks to the install over its REST API. It needs a pairing token. It does not
run any media itself.

## Requirements

Meters poll `GET /api/v1/levels` per programme. A WebSocket is not used for them,
because a levels frame on `/ws` carries no engine id, so on a multi-programme
install the socket interleaves every programme's meters.

- Stream Deck app 7.1 or later, and the plugin runs on Node 24 (the README of
  `@elgato/streamdeck` states both requirements).
- A polyemesis install with the connection-check, stop-impact, recording and
  routing-preset routes (`GET /api/v1/auth/connection`,
  `GET /api/v1/destinations/stop-impact`, `GET` and `POST /api/v1/recording`,
  `GET /api/v1/routing/presets`, `POST /api/v1/routing/presets/{preset}`).
- For the Mute track key and the Track gain dial, an install with live track gain
  (`POST /api/v1/destinations/{id}/tracks/{track}/gain`). They also read
  `GET /api/v1/destinations` and `GET /api/v1/source`.

## Pairing

1. In the install's Settings, create an API token. A read token is enough to
   show state. Start, stop, failover, record, preset, track mute and track gain
   controls need an **admin**
   token, and the plugin says so on the key instead of failing with a 403. A
   read token sends no write request at all.
2. In the Stream Deck app, open any polyemesis action and paste the server
   address and token. Press **Test connection**.
3. For an install with a self-signed certificate, copy the CA from
   `GET /api/v1/tls/ca` and paste it into the **Certificate** field. The CA is
   added to the roots the plugin's own requests trust, alongside Node's bundled
   public roots. The operating system's trust store is not touched.
   Verification is never switched off.

The token is saved in the plugin's global settings only. It is never stored in
action settings, so it does not travel with a profile export.

## Keys

| Action | What it does |
| --- | --- |
| Destination | Shows one destination's live state. Tap to start it when it is off. |
| Go live | Starts every destination on the key's programme. Needs admin. |
| Stop all | Hold to stop every destination on the key's programme. Shows how many YouTube broadcasts a stop would end, and refuses if that count cannot be loaded. Needs admin. |
| Stop destination | Hold to stop one destination. Shows how many YouTube broadcasts that stop would end, read from the server for that destination, and refuses if the count cannot be loaded or the destination is unknown. Needs admin. |
| Record | Shows the install-wide recording switch: **Recording**, **Record off**, **Not running**, or **HALTED: DISK** with the reason when the free-space floor stopped it. Tap to switch. Recording is not per programme, so the key does not take a programme. Needs admin to switch. |
| Routing preset | Hold to apply a routing preset to the key's programme. The preset list comes from the install. Applying can restart that programme's destinations. Shows **No ingest** when the install has no programme to apply to. Needs admin. |
| Chat poll | Starts the chat poll set on the key (a question, two to six options, one per line, and a duration), announced in every connected chat. While a poll runs the key shows **Poll on** with the votes so far and the time left, then **Counting** while the last votes are judged; hold it to end the poll early. Counts only: the key never names a voter. Install-wide, so it takes no programme. Needs admin to start or end. |
| Failover | Shows which source is on air. Tap to hand control back to automatic. Needs admin. |
| Mute track | Shows whether one audio track is muted on one destination: **MUTED** (red, a crossed speaker) or **Unmuted**. Tap to switch. Shows **Not available** for a destination that passes its audio through unchanged (audio copy), and for any change the server refuses, with the server's reason. Needs admin to switch. |
| Track gain (dial) | One audio track's gain on one destination. Turn for 0.5 dB a step, push to mute or unmute, touch the strip for 0 dB. The strip shows the destination and track, the level in dB and a bar, and **MUTED**, **NO FEED** or **matrix: use UI**. Needs admin to change. |
| Meter (dial) | The loudest channel on this install's meters, read from `GET /levels` for the key's programme. Shows NO FEED when the server reports the feed stale or unreachable. |

Record is a tap, not a hold, so one press starts or stops recording for every
programme. Stopping a recording does not end a broadcast; a stop is a hold because
it ends YouTube broadcasts for good.

## Track mute and gain

The Mute track key and the Track gain dial each act on one track of one
destination. In the inspector, pick the programme, then a destination on it,
then a track from that destination's selection. A track is named by its label
from the install's Settings, or by its title, or else by its number. Only the
tracks in the destination's selection are offered, because the server takes no
level for any other.

Both show the level the install has **stored**, read on the same two-second tick
as the other keys. So a mute made in the UI or over the API shows on the key too.
A tap or a push reads it fresh first, so it flips what the install has now.

A change reaches the output in one of two ways, and the key says which for three
seconds:

- **Applied live.** The running FFmpeg took the new level on its command channel.
  Viewers hear the change and nothing else.
- **Applied by restart**, with the server's reason, on its own orange face with a
  circular arrow. A running destination was restarted to take the level, so
  viewers saw a reconnect. A Windows server always restarts, because live level
  changes have no verified path there. A destination that is not running stores
  the level and says so in the reason; it takes effect when the destination starts.

The dial shows a restart the same way, and how a push applied. It does not flash
"Applied live" on every step of a turn.

A matrix mix sets its gain per cell, so the server refuses a track gain there.
The dial says **matrix: use UI** and sends nothing when turned or touched. A mute
still applies to a matrix mix, so the key and a push of the dial still work. A
destination with audio copy on has no level stage at all, so both controls show
**Not available** and send nothing.

The dial steps in decibels, not linear gain. Loudness is heard on a log scale: a
linear step of 0.05 is 0.4 dB at unity but 6 dB near the bottom of the range, so
one tick would be inaudible at the top and a jump at the bottom. A 0.5 dB step is
about the smallest change a listener hears, so every tick does something and
none of them startles an audience. The dial runs from -40 dB to +6 dB. +6 dB is
the highest step under the server's cap of 2.0 (+6.02 dB). Below -40 dB a track
is lost under a programme mix, and the way to silence it is mute, which keeps the
level to come back to.

A fast spin does not send one request per tick. The strip moves at once, and the
request goes a quarter of a second later with wherever the turns have reached.
Only one request is out at a time. A turn made while one is out is sent once,
when it returns, as the latest level.

## Development

```sh
npm ci
npm test          # vitest: the key mapping, the runtime, the client, the manifest
npm run coverage  # the same tests with lcov, read by SonarCloud
npm run typecheck # tsc --noEmit
npm run build     # bundles src/plugin.ts into com.polyemesis.streamdeck.sdPlugin/bin
                  # and writes ui/strings.js from the catalogs
node scripts/icons.mjs   # redraws every icon under com.polyemesis.streamdeck.sdPlugin/imgs
```

The icons are drawn by `scripts/icons.mjs` from shapes, with no image library, and
the PNGs are committed. Each key action has eleven faces at 72 and 144 px, one per
state in `KEY_STATE` order (`src/render.ts`): a tile in the state colour with the
action's glyph in the corner, clear of the title. Held uses the UI's accent
(`--primary`). Five states draw a status mark instead of the action glyph, so they
can never be mistaken for another state: **HALTED: DISK** (a drive, on crimson; only
the Record key uses it), **Needs admin token** (a padlock, on violet), and three
that only the Mute track key uses: **MUTED** (a crossed speaker, on red),
**Applied by restart** (a circular arrow, on orange) and **Not available** (a barred
circle, on grey). Every key action has every face, so the state indices line up
across actions. The action icons are white monochrome glyphs on transparent at 20
and 40 px, the category icon the same at 28 and 56 px. The plugin icon (256 and
512 px) is the emblem in the UI's `--primary-bright` on its dark ground. Both dials
use the built-in `$B1` layout, so there is no encoder background image.

Strings live in `src/locales/*.json`, in the same fifteen locales the UI ships.
The inspector reads `ui/strings.js`, which the build generates from those files.
Stream Deck's own manifest localisation covers only de, en, es, fr, ja, ko,
zh_CN and zh_TW, and the manifest is still English-only.
