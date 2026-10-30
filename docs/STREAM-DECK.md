# Stream Deck

polyemesis has a plugin for the Elgato Stream Deck. Its keys show what each
destination is doing, start or stop destinations, and mute one track on one
destination. On a Stream Deck+, dials show the audio meters and set a track's
gain. It talks to the install over the
[REST API](API.md) with a token you paste in, and it runs no media itself.

The plugin's source, and the notes for working on it, are in
[`integrations/streamdeck`](../integrations/streamdeck/README.md).

- [Requirements](#requirements)
- [Install](#install)
- [Pairing](#pairing) — a read token or an admin token
- [A self-signed certificate](#a-self-signed-certificate)
- [The keys](#the-keys)
- [Troubleshooting](#troubleshooting)

---

## Requirements

- **Stream Deck app 7.1 or later**, on macOS 12+ or Windows 10+. The plugin
  runs on the Node 24 runtime the app ships.
- **A Stream Deck+** for the Meter and Track gain dials. Every other action is a key and works
  on any model.
- **A polyemesis install from this release or later.** The plugin relies on
  `GET /api/v1/auth/connection`, `GET /api/v1/destinations/stop-impact`,
  `GET` and `POST /api/v1/recording`, the `feed` field on
  `GET /api/v1/levels`, and, for the Mute track key and Track gain dial,
  `POST /api/v1/destinations/{id}/tracks/{track}/gain` along with the
  `GET /api/v1/destinations` and `GET /api/v1/source` reads. An older install does not have them, and Test
  connection fails against it.
- **HTTPS to the install**, unless the plugin runs on the same machine. The
  plugin refuses a plain `http://` address for any host but `localhost`,
  `127.0.0.1` or `::1`, because the token is a bearer credential.

## Install

No packaged plugin is published yet, so it is built from the repository. You
need Node 24 and npm, and Elgato's Stream Deck CLI (`npm install -g
@elgato/cli`).

```sh
cd integrations/streamdeck
npm ci
npm run build
streamdeck link com.polyemesis.streamdeck.sdPlugin
```

`npm run build` bundles the plugin into
`com.polyemesis.streamdeck.sdPlugin/bin`. `streamdeck link` installs that
folder into the Stream Deck app. The **polyemesis** category then appears in
the app's action list. After a rebuild, `streamdeck restart
com.polyemesis.streamdeck` reloads it.

## Pairing

The plugin keeps one server address and one token for every key. They are
stored in the plugin's global settings and never in a key's own settings, so
the token does not travel with a profile export.

1. **Create a token.** In polyemesis, open **Settings** and create an API token.
   Choose the scope by what the deck will do:

   | Scope | What the plugin can do with it |
   |---|---|
   | `read` | Show state: destination faces, the failover source, the Record switch, the meters. A key that would change something shows **Needs admin token**, and the plugin sends no write request at all. |
   | `admin` | Everything above, and start, stop, Go live, Record, Routing preset, Failover, Mute track and Track gain. |

   An admin token can do anything the signed-in operator can, through the API,
   not only what the keys do. If the deck only has to show state, give it a read
   token. [MONITORING.md](MONITORING.md#automation) has the token rules.

2. **Paste it.** In the Stream Deck app, drag any polyemesis action onto a key
   and open its settings. Enter the **Server address** (for example
   `https://stream.example.com`) and paste the **API token**.

3. **Press Test connection.** The plugin calls `GET /api/v1/auth/connection`,
   which answers for the pasted token only: its name, its prefix, its scope,
   and whether it is admin. The inspector shows **Connected**. With an admin
   token it also says that the token can start and stop destinations.
   Settings in polyemesis has the same Test connection check for a token you
   are about to hand to a device.

4. **Bind each key.** On an install with more than one programme, enter the
   **Programme ID** a key acts on. Leave it empty on a single-programme
   install. A Destination or Stop destination key also takes a
   **Destination ID**. A Routing preset key picks its preset from the list the
   install serves.

## A self-signed certificate

An install in `selfsigned` TLS mode signs its own certificate with a local CA,
and the plugin will not trust it until you give it that CA. The plugin never
switches certificate verification off.

1. Download the CA from the install: `https://<install>/api/v1/tls/ca`. The route
   needs no sign-in, for the same reason the browser needs it before the login
   page will load.
2. Paste the whole PEM, including the `BEGIN` and `END` lines, into the
   **Certificate (optional)** field and press **Test connection** again.

The pasted CA is added to the roots the plugin itself trusts, alongside the
public ones that ship with Node. It is not installed into the operating
system's trust store, and it affects no other program. Leave the field empty
for an install with a publicly trusted certificate.

[TLS.md](TLS.md#trusting-the-self-signed-ca) covers the CA itself and
[what trusting it grants](TLS.md#what-trusting-the-ca-grants).

## The keys

A stop is a **hold**, because stopping a YouTube destination ends its broadcast
for good. Press and hold for 1.5 seconds. The key loads the count of YouTube
broadcasts the stop would end when you press it, and releasing early cancels.
Every other key acts on a tap, except Routing preset, which is also a hold.

| Action | Tap or hold | What it does | Needs |
|---|---|---|---|
| **Destination** | Tap | Shows one destination's live state. Tapping starts it when it is off. | Read to show; admin to start |
| **Go live** | Tap | Starts every destination on the key's programme. | Admin |
| **Stop all** | Hold | Stops every destination on the key's programme. Shows how many YouTube broadcasts the stop would end. If that count cannot be loaded, it refuses. | Admin |
| **Stop destination** | Hold | Stops one destination. Shows whether the stop would end a YouTube broadcast, read from the server for that destination. It refuses if the count cannot be loaded or the destination is not the key's own. | Admin |
| **Record** | Tap | Shows the install-wide recording switch and toggles it. Recording is one switch for every programme, so this key takes no programme. Stopping a recording ends no broadcast, so it is a tap. | Read to show; admin to switch |
| **Routing preset** | Hold | Applies a routing preset to the key's programme. Applying one can restart that programme's destinations. | Admin |
| **Failover** | Tap | Shows which source is on air: primary, backup, playlist or slate. Tapping hands control back to automatic. | Read to show; admin to switch |
| **Mute track** | Tap | Mutes or unmutes one track on one destination. See [Track mute and the gain dial](#track-mute-and-the-gain-dial). | Read to show; admin to switch |
| **Meter** (dial) | — | The loudest channel on the key's programme, read from `GET /api/v1/levels` five times a second. | Read |
| **Track gain** (dial) | Turn, push, touch | One track's gain on one destination. See below. | Read to show; admin to change |

### Track mute and the gain dial

The **Mute track** key and the **Track gain** dial each act on one audio track
of one destination. Both call `POST /api/v1/destinations/{id}/tracks/{track}/gain`
([API.md](API.md)), and both need an admin token to change anything.

**Binding.** In the key's settings, pick the programme, then a destination on
it, then a track. Only the tracks in that destination's selection are offered,
because the server takes no level for any other. A track is named by its label
from the install's Settings, by its title, or by its number.

**What they show.** Both show the level the install has *stored*, read on the
same two-second tick as the other keys, so a mute made in the UI or over the API
shows on the deck too. A tap or a push reads the level fresh first, so it flips
what the install has now, not what the key last showed.

**The Mute track key.** It shows **MUTED** (a crossed speaker, on red) or
**Unmuted**. Tap to switch.

**The Track gain dial.** It needs a Stream Deck+.

| Control | Does |
|---|---|
| Turn | 0.5 dB a step, from -40 dB to +6 dB |
| Push | Mute or unmute |
| Touch the strip | Back to 0 dB |

The strip shows the destination and track, the level in dB, and a bar. The dial
steps in decibels, not linear gain, so every tick is an audible change and none
is a jump. +6 dB is the highest step under the server's cap of 2.0 (+6.02 dB). To
silence a track, mute it: the level it had is kept for when you unmute. A fast
spin sends one request a quarter of a second after the turning stops, not one
per tick.

**How a change applied.** After a change, the key shows for three seconds which
way it reached the output:

- **Applied live.** The running FFmpeg took the new level. Viewers hear the
  change and nothing else.
- **Applied by restart**, with the server's reason, on an orange face with a
  circular arrow. The destination was restarted to take the level, so viewers
  saw a reconnect. **A Windows server always restarts**, because live level
  changes have no verified path there. A destination that is not running stores
  the level, and the reason says it takes effect when the destination starts.

The dial shows a restart the same way, and how a push applied, but it does not
flash Applied live on every step of a turn.

**When they do nothing.**

| Face | Why |
|---|---|
| **Not available** (a barred circle, on grey) | The destination passes its audio through unchanged (audio copy), so there is no level to set. Also shown, with the server's reason, for a change the server refused |
| **matrix: use UI** (dial only) | A matrix mix sets its gain per cell, and the server refuses a track gain there. Turning and touching send nothing. A push still mutes, and so does the Mute track key |
| **Track not in mix** | The track is no longer in the destination's selection |
| **Needs admin token** | The token is `read` |
| **NO FEED** (dial only) | The install is unreachable, or its stored levels could not be read. The dial never holds an old level on screen |

How the server applies a level live is in
[HOT-RELOAD.md](HOT-RELOAD.md#live-per-track-gain-and-mute-the-stdin-command-channel).

### What the faces mean

**Destination**

| Face | Meaning |
|---|---|
| Live | The destination's process is running and sending |
| Starting, Reconnecting | Coming up, or retrying after a drop |
| Stalled | Running, but not making progress |
| Failed | Stopped on an error. The second line names the cause the server classified (**Key rejected**, **Not ready**, **DNS failed**, **Refused**, **Timed out**, **TLS failed**, **Codec rejected**, **Unknown** with FFmpeg's own line), or **Key changed** when the connected account reports a different stream key |
| Held | The server stopped retrying after repeated quick key or codec rejections. Press Retry on the destination card to try again |
| Live, Upload slow | Running, but delivering slower than realtime |
| Off | Not running |
| Stop warning | The last stop timed out and the process was killed without being seen to exit, so it may still have been publishing; the detail says why |
| Backup down | The destination's redundant feed is not running |
| Not found | The destination ID names nothing on this install |
| Other programme | The destination belongs to a programme other than the key's |

**Stop keys:** **Hold to stop**, then **{n} YouTube will end** or **No YouTube
broadcasts** once the count has loaded. **Count unavailable** means the stop
was refused because the count could not be read.

**Record:** **Recording**, **Record off**, **Not running** (switched on, but no
recorder is up, for example because no programme has started), or **HALTED:
DISK** with the reason, when the free-space floor has stopped the recorder.
HALTED: DISK has its own image, a drive on crimson.

**Routing preset:** **Hold to apply**, **Applying**, or **No ingest** when the
install has no programme to apply it to.

**Failover:** **Primary on air**, **Backup on air**, **Playlist on air**,
**Slate on air**, or **No feed info**.

**Meter:** a bar from -60 dBFS to full scale, **QUIET** at digital silence, or
**NO FEED** when the server says the feed is not live. The plugin does not
judge freshness itself. It draws what `feed` says: `none` before any reading,
`stale` once readings stop.

## Troubleshooting

| On the key or in the inspector | Cause | Fix |
|---|---|---|
| **Not paired** | No server address or token saved | Open any polyemesis action's settings and pair it |
| **Token refused** | The install answered `401`: the token is wrong, revoked or expired | Mint a new token in Settings and paste it. A refused token is not retried on its own |
| **Unreachable** | Nothing answered within 5 seconds, or a status read failed | Check the address and the network. The plugin retries every 2 seconds, and a key never keeps a green face while the install is down |
| **Unreachable**, and Test connection adds "the server's certificate is not trusted; paste the install's CA" | The install's certificate is signed by a CA the plugin does not trust | Paste the install's CA, as in [A self-signed certificate](#a-self-signed-certificate) |
| **Needs admin token** (a padlock, on violet) | The key changes something and the token is `read` | Pair with an admin token, or keep the read token and use the key for state only |
| "Use https:// for the server" | A plain `http://` address for a host other than this machine | Use the install's `https://` address |
| **Not found** or **Other programme** on a Destination key | The Destination ID or Programme ID does not match the install | Correct the IDs in the key's settings |
| **Count unavailable** on a stop key | The YouTube count could not be loaded, so the stop was refused | Use the polyemesis UI to stop, or retry once the install answers |
| **NO FEED** on the dial | The meters have no current reading: no ingest, the meter sidecar is down, or the wrong programme | Check the source in polyemesis; on a multi-programme install, check the dial's Programme ID |
| "The preset list could not be loaded." | The preset list request failed | Test the connection, then reopen the key's settings |

A key never shows a cached Live face while the install is unreachable: a failed
status read drops the cache and the key paints Unreachable.

## See also

- [integrations/streamdeck/README.md](../integrations/streamdeck/README.md) —
  building, testing and the icon set
- [API.md](API.md) — the routes the plugin calls
- [TLS.md](TLS.md#trusting-the-self-signed-ca) — the install's CA
- [OBS.md](OBS.md) — the encoder side
