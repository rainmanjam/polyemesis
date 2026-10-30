# polyemesis Stream Deck plugin

An Elgato Stream Deck plugin for a polyemesis install. It shows what each
destination is really doing, starts and stops destinations, and shows the
audio meters on a Stream Deck+ dial.

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

## Pairing

1. In the install's Settings, create an API token. A read token is enough to
   show state. Start, stop, failover, record and preset keys need an **admin**
   token, and the plugin says so on the key instead of failing with a 403. A
   read token sends no write request at all.
2. In the Stream Deck app, open any polyemesis action and paste the server
   address and token. Press **Test connection**.
3. For an install with a self-signed certificate, copy the CA from
   `GET /api/v1/tls/ca` and paste it into the **Certificate** field. The CA is
   added to the system trust store. Verification is never switched off.

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
| Failover | Shows which source is on air. Tap to hand control back to automatic. Needs admin. |
| Meter (dial) | The loudest channel on this install's meters, read from `GET /levels` for the key's programme. Shows NO FEED when the server reports the feed stale or unreachable. |

Record is a tap, not a hold, so one press starts or stops recording for every
programme. Stopping a recording does not end a broadcast; a stop is a hold because
it ends YouTube broadcasts for good.

Track mute is coming with live gain (I4).

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
the PNGs are committed. Each key action has eight faces at 72 and 144 px, one per
state in `KEY_STATE` order (`src/render.ts`): a tile in the state colour with the
action's glyph in the corner, clear of the title. Held uses the UI's accent
(`--primary`). Two states draw a status mark instead of the action glyph, so they
can never be mistaken for another state: **HALTED: DISK** (a drive, on crimson; only
the Record key uses it) and **Needs admin token** (a padlock, on violet). The action
icons are white monochrome glyphs on transparent at 20 and 40 px, the category icon
the same at 28 and 56 px. The plugin icon (256 and 512 px) is the emblem in the UI's
`--primary-bright` on its dark ground. The dial uses the built-in `$B1` layout, so
there is no encoder background image.

Strings live in `src/locales/*.json`, in the same fifteen locales the UI ships.
The inspector reads `ui/strings.js`, which the build generates from those files.
Stream Deck's own manifest localisation covers only de, en, es, fr, ja, ko,
zh_CN and zh_TW, and the manifest is still English-only.
