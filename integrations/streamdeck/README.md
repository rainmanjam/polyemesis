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
- A polyemesis install with the connection-check and stop-impact routes
  (`GET /api/v1/auth/connection`, `GET /api/v1/destinations/stop-impact`).

## Pairing

1. In the install's Settings, create an API token. A read token is enough to
   show state. Start, stop and failover keys need an **admin** token, and the
   plugin says so on the key instead of failing with a 403.
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
| Failover | Shows which source is on air. Tap to hand control back to automatic. Needs admin. |
| Meter (dial) | The loudest channel on this install's meters, read from `GET /levels` for the key's programme. Shows NO FEED when the server reports the feed stale or unreachable. |

Track mute is not included. The engine cannot change a track's gain without
restarting the destination's stream, and that decision is still open.

## Development

```sh
npm ci
npm test          # vitest: the key mapping, the runtime, the client, the manifest
npm run coverage  # the same tests with lcov, read by SonarCloud
npm run typecheck # tsc --noEmit
npm run build     # bundles src/plugin.ts into com.polyemesis.streamdeck.sdPlugin/bin
                  # and writes ui/strings.js from the catalogs
node scripts/icons.mjs   # regenerates the placeholder PNG icons
```

The icons are flat colour placeholders. They are not final artwork.

Strings live in `src/locales/*.json`, in the same fifteen locales the UI ships.
The inspector reads `ui/strings.js`, which the build generates from those files.
Stream Deck's own manifest localisation covers only de, en, es, fr, ja, ko,
zh_CN and zh_TW, and the manifest is still English-only.
