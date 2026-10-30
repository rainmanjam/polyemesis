# OBS and Streamer.bot

The OBS bridge lets OBS drive polyemesis:

- **OBS starts streaming:** the destinations you picked go live.
- **OBS stops streaming:** they stop, if you have turned that on. You can add
  a grace period, so a quick restart of OBS's stream does not end anything.
- **OBS switches scene:** a routing preset you mapped to that scene is applied
  live. For example, "Just Chatting" can drop the music track on Twitch.

Polyemesis can learn about OBS in two ways. Pick the one your network allows:

| | Polyemesis connects to OBS | OBS reports to polyemesis |
|---|---|---|
| How | Polyemesis dials obs-websocket, the WebSocket server built into OBS 28 and later | The polyemesis script inside OBS posts each change to polyemesis |
| Use it when | Polyemesis can reach the streaming PC: on the same LAN, or over Tailscale | It cannot, such as polyemesis on a VPS with no tunnel back |
| Credential | The obs-websocket password, stored sealed on polyemesis | An API token with the `obs` scope, stored in OBS |
| Connection state in the console | Yes | No; OBS reports, polyemesis only listens |

Running both is safe. Polyemesis acts on changes, and the second report of the
same change does nothing.

Polyemesis never changes anything in OBS. It sends obs-websocket two read-only
requests, for the stream state and the current scene, and only after it
connects.

## Set up: polyemesis connects to OBS

1. In OBS, open **Tools → WebSocket Server Settings**. Tick **Enable
   WebSocket server**, keep **Enable Authentication** on, and copy the
   password with **Show Connect Info**. The port is 4455 unless you changed it.
2. In polyemesis, open **Settings → OBS bridge**:
   - Turn the bridge on, and turn on **Connect to obs-websocket**.
   - Set the address to `ws://<streaming PC>:4455`.
   - Paste the password.
   - Pick the programme and the destinations OBS should drive.
3. Save. The connection state shows **connected** within a few seconds.

### On the same LAN

Use the streaming PC's LAN address, such as `ws://192.168.1.20:4455`. Give that
PC a fixed address (a DHCP reservation on your router), or the bridge loses OBS
the next time the lease changes. Allow inbound TCP 4455 in the PC's firewall,
from the polyemesis host only if your firewall can do that.

### Over Tailscale

When polyemesis and the streaming PC are on the same tailnet, use the PC's
Tailscale name or address: `ws://studio-pc.<tailnet>.ts.net:4455` or
`ws://100.x.y.z:4455`. Nothing has to be opened on your router, and the
traffic is encrypted by Tailscale end to end. If you use Tailscale ACLs, allow
the polyemesis node to reach the streaming PC on TCP 4455.

Do not expose port 4455 to the internet. obs-websocket can do anything to OBS,
and its password is the only lock on it. If polyemesis cannot reach OBS
privately, use the script instead.

`wss://` works when you put obs-websocket behind a TLS proxy of your own. OBS
itself only serves `ws://`.

### Connection states

| State | Meaning |
|---|---|
| `off` | The bridge is off, or it is not set to connect (script only). |
| `connecting` | Dialling, or waiting before the next attempt. |
| `connected` | Connected and receiving events. |
| `unreachable` | Nothing answered, or the link dropped. Polyemesis retries, backing off to every 30 seconds. |
| `refused` | obs-websocket refused the password, or asked for one and none is set. Retried every 30 seconds. |
| `incompatible` | Something answered that is not obs-websocket 5. |

**A dropped connection never stops a destination.** OBS crashing, the LAN
going down, or polyemesis restarting is not you stopping the stream. When the
link comes back, polyemesis reads where OBS is and acts on nothing it reads
that way. If OBS stopped streaming while the link was down, stop the
destinations yourself.

## Set up: OBS reports to polyemesis

1. In polyemesis, **Settings → API tokens**, create a token with the **obs**
   scope. It can do one thing: report OBS events. It cannot read anything,
   change a setting, or start a destination itself.
2. In polyemesis, **Settings → OBS bridge**, turn the bridge on, leave
   **Connect to obs-websocket** off, and pick the programme and destinations.
3. The script is Python. In OBS, open **Tools → Scripts → Python Settings** and
   point it at a Python 3 install. OBS on Windows needs a 64-bit Python whose
   version OBS supports; the Python Settings tab says which. Lua could not do
   this: OBS's Lua has no HTTP client.
4. In **Tools → Scripts**, press **+** and choose
   [`integrations/obs/polyemesis_obs.py`](../integrations/obs/polyemesis_obs.py).
5. Fill in the polyemesis address (`https://host:8080`) and the token.
   - If your install uses its own self-signed certificate, download the CA from
     `GET /api/v1/tls/ca` and choose it as **CA certificate**. Verification is
     never switched off.
   - **Report scene switches** is on by default.

OBS keeps script settings, the token included, in plain text in your scene
collection's file. That is why the script takes an `obs` token and never an
admin one.

The script sends from a background thread, so a slow or unreachable polyemesis
never freezes OBS. A failed report is retried twice and then given up, and the
OBS script log says why, without the token. A `409` means the bridge is
switched off in polyemesis.

## Starting and stopping

**Start** is the same as pressing start on each destination you picked, two
seconds apart. If OBS stops while the start is still working through the list,
the rest are left alone and marked as skipped.

**Stop** is off until you turn it on, and it then asks you to confirm.
Stopping a destination ends its YouTube broadcast for good. Starting again
puts video back on the wire, but it does not bring that broadcast back. That
is the warning **Stop all** gives each time you press it. The bridge asks for
it once, when you turn automatic stopping on. Then:

- Only the destinations you picked are stopped, never every destination, and
  never one that has moved to another programme.
- With a grace period, polyemesis waits that many seconds after OBS stops. If
  OBS starts streaming again within the grace period, nothing is stopped.
- OBS reconnecting (`OBS_WEBSOCKET_OUTPUT_RECONNECTING`) is not a stop.

## Scene rules

A scene rule maps one OBS scene name, matched exactly, to one routing preset.
It applies to all the bridge's destinations, or to the ones you name.

A switch is applied **live** and never restarts a destination. Polyemesis uses
the preset only to choose which tracks are heard, and applies that as a live
mute or unmute on the tracks already in each destination's mix. This is the
same path as the routing page's mute button and the Stream Deck's mute key.
Gains, loudness and delay are left as you set them. A rule is skipped, and the
status says why, when:

- the preset wants a track the destination does not carry, which would need a
  restart. The other tracks are still applied, and a note says which track
  was left out.
- the preset keeps none of the destination's tracks, which would silence it.
- the destination copies its audio unchanged.
- polyemesis runs on Windows, where track levels cannot change live.

What each switch did, per destination, is under **Last actions** in Settings,
and in `status.lastActions` on `GET /api/v1/obs`.

## Events

The bridge publishes `obs.connection`, `obs.stream` and `obs.scene` on the
[events API](EVENTS.md#obs), and they reach webhooks' subscribers the same
way. The destinations it starts and stops produce the usual `destination.up` and
`destination.down`.

## Streamer.bot

The pack in [`integrations/streamerbot`](../integrations/streamerbot) gives
Streamer.bot actions that call the polyemesis REST API with a token:

- go live
- stop all
- start or stop one destination
- mute or unmute a track
- recording on or off
- a status line for chat

It needs no changes on the polyemesis side, and polyemesis stores nothing for
it. See its [README](../integrations/streamerbot/README.md) for setup. The
token lives in a persisted Streamer.bot global, not in the action. An exported
action therefore does not carry it.

### Mix It Up

Mix It Up can make the same calls with its web request action: the method,
path and body for each are in
[`actions.json`](../integrations/streamerbot/actions.json). Send the token as
`Authorization: Bearer <token>`. Polyemesis ships no importable Mix It Up
file, and none of this has been tried in Mix It Up.

## API

`GET` and `PUT /api/v1/obs`, `PUT /api/v1/obs/password` and `POST
/api/v1/obs/events` are described in [API.md](API.md#obs-bridge).

## What has been tested

Polyemesis's tests run against a mock obs-websocket 5 server, written from the
protocol document and from the plugin's authentication code. They do not run
against OBS itself. The OBS script is run under a stand-in for OBS's Python
module. The Streamer.bot pack's requests are replayed against the real API, but
not from inside Streamer.bot.
