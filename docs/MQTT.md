# MQTT telemetry

polyemesis publishes its state to an MQTT broker as **retained** messages, so a
consumer that was not connected when something changed still gets the current
answer the moment it subscribes.

That is the whole point, and it is the one thing the other monitoring paths
cannot do. Prometheus scrapes on its own schedule; the WebSocket needs a live
browser; a webhook fires once and is gone. None of them can answer *"is the
ingest up?"* to a Home Assistant instance that restarted five minutes ago.

- [Before you start](#before-you-start)
- [Setting it up](#setting-it-up)
- [The topic tree](#the-topic-tree)
- [Payloads](#payloads)
- [Home Assistant](#home-assistant)
- [Node-RED and everything else](#node-red-and-everything-else)
- [What is deliberately not published](#what-is-deliberately-not-published)
- [Troubleshooting](#troubleshooting)

---

## Before you start

**polyemesis speaks MQTT 5.0 only.** The client library implements the 5.0
specification and nothing earlier, so a broker pinned to 3.1.1 will not complete
a connection at all — it is not a degraded mode, it is a failure to connect.
Mosquitto 2.x, EMQX, HiveMQ and the Home Assistant Mosquitto add-on all speak
5.0 and are fine.

| | |
|---|---|
| **Protocol** | MQTT 5.0 only |
| **Transports** | `mqtt://`, `mqtts://`, `ws://`, `wss://` |
| **QoS** | 1 on every message, never 0 |
| **Retain** | set on every message |
| **Offline buffering** | none, by design |

**Everything is QoS 1 on purpose.** A conforming broker *may decline to store* a
retained QoS 0 message, and state that must survive a broker restart therefore
has to be QoS 1. This is not tuning; at QoS 0 the feature would fail silently on
exactly the restart it exists to survive.

**There is no offline buffering, also on purpose.** When the broker is
unreachable a reading is dropped rather than queued. A ninety-second-old bitrate
replayed on reconnect is worse than no reading at all, because the next tick
republishes ground truth anyway — and in between, a dashboard would be showing a
number that was never true at the time it appeared.

## Setting it up

`Settings → MQTT telemetry`. Off by default; an upgrade never starts publishing to a
broker nobody configured.

| Field | Notes |
|---|---|
| **Broker URL** | `mqtt://host:1883`, `mqtts://host:8883`, `ws://`, `wss://`. **Credentials in the URL are refused** — see below |
| **Username / password** | Optional. The password is encrypted at rest and never returned by any API |
| **Prefix** | Roots the tree, default `polyemesis`. Separators are preserved: `home/av` means two levels |
| **Instance** | Distinguishes two installs sharing one broker, and keys the Home Assistant device |
| **Client ID** | Must be unique on the broker. Leave empty to derive one from the instance |
| **Interval** | How often state is checked, default 10s. Unchanged state is not republished |
| **Keep-alive** | Default 30s. This is what bounds how long a dead link goes unnoticed |
| **Home Assistant discovery** | On by default. Turn it off for a Node-RED or Telegraf consumer |

**A password in the broker URL is refused rather than accepted.** A URL reaches
log lines, `ps` output and error strings, and there is no taking it back
afterwards. Put the username and password in their own fields, where the
password is sealed.

**A topic prefix beginning with `$` is refused too.** Brokers reserve
`$`-prefixed topics for their own metrics, and a subscriber using `#` — which is
what anyone debugging reaches for first — is specified never to receive them.
The telemetry would publish successfully, be acknowledged, and be invisible in
exactly the view you would use to look for it.

Changes take effect within about five seconds. No restart.

## The topic tree

```
polyemesis/<instance>/status                              "online" | "offline"
polyemesis/<instance>/state                               host JSON
polyemesis/<instance>/source/<slug>/state                 per-source JSON
polyemesis/<instance>/source/<slug>/dest/<slug>/state     per-destination JSON
polyemesis/<instance>/source/<slug>/rendition/<slug>/state per-rendition JSON
homeassistant/device/<instance>/config                    HA discovery
```

Every one is retained and QoS 1.

**`status` is the availability topic**, and it is the reason the rest is
trustworthy. polyemesis registers it as a *will message* when it connects, so if
the process dies without disconnecting — power cut, OOM kill, `kill -9` — the
**broker** publishes `offline` on its behalf once the keep-alive expires. On a
clean shutdown polyemesis publishes `offline` itself, because a proper
DISCONNECT causes the broker to discard the will.

Without this, a dead instance's dashboard keeps showing its last reading
indefinitely: confidently wrong, which is worse than showing nothing.

### About the slugs

Names are lowercased and reduced to `[a-z0-9_-]`. **If the reduction changed the
name at all, eight hex characters of its hash are appended.**

That is not decoration. `Twitch (main)` and `Twitch [main]` both reduce to
`twitch-main`, and without the hash one destination's retained state would
permanently overwrite the other's — with a symptom (an entity flickering between
two streams' numbers) that looks nothing like its cause.

A name that needs no changing is left alone, so `twitch` stays `twitch`.

Two edge cases fall out of that rule and are handled rather than left to chance.
A name that reduces to nothing at all — `!!!` — becomes `x` plus its hash rather
than an empty topic segment. And a name that *already* ends in something shaped
like a generated suffix, such as `stream-a1b2c3d4`, is hashed too even though
the reduction changed nothing: otherwise an operator could pick a literal name
that collides with another entity's generated one, which is the same overwrite
the hash exists to prevent.

### Orphans are cleaned up

Delete or rename a source or destination and its retained topic is cleared with
a zero-length message, which is the specified way to delete one. Without that
sweep the broker would hold its last state forever, with a Home Assistant entity
still attached.

## Payloads

> **`uptimeSec` changed meaning in v0.8.0.** It now counts from the moment media
> first arrived on that process, not from when the process was spawned. An
> ingest is started *listening*, so the old figure included however long it sat
> waiting for an encoder to connect — arming a source in the morning and going
> live at noon reported four hours of uptime for a stream that had been on air
> for none. A listening ingest now reports `live: false` and `uptimeSec: 0`,
> which is what it is doing. `startedAt`, where it appears, is unchanged and
> still reports the spawn.

```jsonc
// polyemesis/studio/source/cam-1/state
{
  "id": 1, "name": "Cam 1", "slug": "cam-1",
  "live": true,              // bytes arriving on the relay, not process state
  "ingestMode": "srt",
  "bitrateKbps": 6120.4,
  "uptimeSec": 3812.5,       // since media FIRST ARRIVED, not since spawn
  "restarts": 0,
  "lossPercent": 0,          // MPEG-TS continuity-counter loss
  "recording": true,
  "destinations": 3, "destinationsUp": 3,
  "at": "2026-07-28T20:14:02Z"
}
```

`live` deserves a note: it is **bytes on the relay**, not process state. An SRT
or RTMP listener sits in "running" for as long as it waits for a publisher,
which is a different question from whether anything is arriving.

## Home Assistant

With discovery on, polyemesis publishes one **device** payload describing every
entity. Home Assistant groups them under a single device card, so removing the
instance removes the lot rather than leaving orphans behind.

You get, per install:

- **Sources live** and **Destinations up** — counts
- per source: a **live** binary sensor and a **bitrate** sensor
- per destination: a **running** binary sensor

Every entity's availability is wired to the same `status` topic the will message
writes, so when polyemesis stops they all go `unavailable` together instead of
freezing on their last value.

If you use the Mosquitto add-on, point the broker URL at
`mqtt://homeassistant.local:1883` and use the add-on's username and password.

`mqtt://` is **unencrypted**, and that is worth stating plainly rather than
leaving implied. MQTT sends the username and password in its CONNECT packet, so
both cross the network in the clear. polyemesis encrypts the password at rest
and refuses one embedded in the URL, neither of which changes what goes on the
wire. On the trusted LAN this add-on assumes, that is the normal and expected
setup. Over anything you do not control, use `mqtts://` — the broker URL field
warns when it sees a plaintext scheme.

## Node-RED and everything else

Turn discovery off and subscribe to `polyemesis/+/#`. Payloads are plain JSON
with stable field names. `status` is a bare word rather than JSON specifically
so an availability template needs no `value_template`.

## What is deliberately not published

**No URL, stream key, token, passphrase or password appears on any topic.**

Two mechanisms, and the second exists because the first was not enough.

The payloads are built from a fixed whitelist of fields rather than by
marshalling a database row, and a test fails if anyone adds a field to one of
them. That stops a *new* field smuggling a credential out.

**It does not stop an approved field carrying one, and one did.** `ingestError`
is FFmpeg's own text, and FFmpeg prints the whole publish URL — stream key
included — when a destination refuses it. The field was on the whitelist, the
guard passed, and the key went to the broker **retained**: readable by every
subscriber with no session, and outliving the process that sent it. The
whitelist test had even exempted the field by name, on the reasoning that an
error is not a URL.

So free-text fields are now masked **where the line is captured**, in
`internal/supervisor`, before anything can copy them. `alerts.RedactURL` — the
same masking applied to webhook alerts — is what does it, so there is one
definition rather than a second one that can drift.

The rule the two give together: a field reaches a topic only if it is on the
whitelist, and any field that can carry operator-authored or tool-authored text
arrives already masked.

**Upgrading from 0.2.0 or earlier:** clear your retained topics. Upgrading stops
new keys being published; it cannot unpublish what your broker is already
holding.

A destination contributes its name, platform and error. Nothing else.

## Troubleshooting

**Nothing appears on the broker.** Check `status` first with
`mosquitto_sub -h HOST -t 'polyemesis/#' -v`. If even that is absent, polyemesis
never connected — the log line is `mqtt connect failed`. The most common causes
are a broker that only speaks 3.1.1 and a firewall.

**It connects, then disconnects, forever.** Almost always a duplicate client ID.
Two clients sharing one ID cause the broker to disconnect the older session on
every connect, and both reconnect immediately. Set an explicit, distinct client
ID on each install.

**Entities show `unavailable` in Home Assistant.** The `status` topic reads
`offline` or is missing. If polyemesis is running, check that the keep-alive is
not 0 and that the broker is not rejecting the will message.

**Stale entities for things I deleted.** Retained messages outlive the process
that sent them; the sweep clears topics polyemesis knows about. If you changed
the prefix or instance name, the old tree is orphaned by definition — clear it
with `mosquitto_sub -h HOST -t 'OLD/#' --remove-retained`.

**Values look frozen.** Unchanged state is not republished, by design. Check the
`at` field: if it is old and the state is genuinely unchanged, that is correct
behaviour.

---

## See also

- [MONITORING.md](MONITORING.md) — Prometheus, webhooks and the alert rules
- [SECURITY.md](../SECURITY.md) — how the broker password is stored
- [../docs/roadmap/MQTT.md](roadmap/MQTT.md) — the design, and what it got wrong
