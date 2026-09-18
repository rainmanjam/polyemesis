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
| **Transports** | `mqtt://`, `mqtts://`, `ws://`, `wss://`, `tcp://`, `ssl://` |
| **QoS** | 1 on every message, never 0 |
| **Retain** | set on every message |
| **Offline buffering** | none, by design |

**Six URL schemes are accepted, not four.** `tcp://` and `ssl://` are the
spellings other MQTT tooling emits, and a config migrated from one of them is
taken as-is rather than rejected for cosmetic reasons. They are not second-class:
`ssl://` gets the same TLS configuration `mqtts://` does — TLS 1.2 minimum, with
certificate verification on — and `tcp://` is plaintext exactly like `mqtt://`.
A seventh scheme is refused with `mqtt broker scheme "amqp" is not one of mqtt,
mqtts, tcp, ssl, ws or wss`.

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
| **Broker URL** | `mqtt://host:1883`, `mqtts://host:8883`, `ws://`, `wss://`, `tcp://`, `ssl://`. **Credentials in the URL are refused** — see below |
| **Username / password** | Optional. The password is encrypted at rest and never returned by any API |
| **Prefix** | Roots the tree, default `polyemesis`. Separators are preserved: `home/av` means two levels. At most 128 characters, and no `+`, `#` or NUL |
| **Instance** | Distinguishes two installs sharing one broker, and keys the Home Assistant device. At most 64 characters |
| **Client ID** | Must be unique on the broker. At most 128 characters. Leave empty to derive `polyemesis-<slugged instance>` |
| **Interval** | How often state is checked, default 10s, range 1–3600s. Unchanged state is not republished. **Editing this alone has no effect** — see below |
| **Keep-alive** | Default 30s, range 1–65535s. This is what bounds how long a dead link goes unnoticed |
| **Accept a self-signed certificate** | Off by default. Turns certificate verification off for `mqtts://`, `ssl://` and `wss://` — see below |
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

**The bounds are enforced on save, and they refuse the whole page.** Settings
are validated as one document, so an out-of-range MQTT field returns 400 and
nothing on the page is written — including the unrelated settings you edited in
the same visit. The messages name the limit they hit:

```
mqtt publish interval 7200s out of range (1-3600)
mqtt keep-alive 0s out of range (1-65535); 0 disables the liveness check the will message depends on
mqtt topic prefix is 190 characters (maximum 128)
mqtt instance name is 96 characters (maximum 64)
mqtt client id is 160 characters (maximum 128)
```

A keep-alive of 0 is legal on the wire and means "no keep-alive". It is refused
here because it would leave a half-open connection looking healthy forever and
the will message — the entire availability story — would never fire. The
interval ceiling is an hour for a related reason: past that a retained topic is
a historical record rather than telemetry.

**`Accept a self-signed certificate` turns off the check `mqtts://` was for.**
It sets `InsecureSkipVerify` on the TLS configuration, so the broker's
certificate is not checked against any chain and its hostname is not checked at
all — the connection is still encrypted, but it is no longer authenticated, and
anything that can sit in the path can terminate it. The minimum TLS version
stays 1.2 either way. Turn it on for a homelab Mosquitto you issued a
certificate to yourself; leave it off for anything crossing a network you do not
own, where the honest fix is to put the broker's CA in the host trust store
instead. The toggle does nothing for `mqtt://`, `tcp://` or `ws://`, which have
no TLS to skip.

Settings are re-read every five seconds, and a change to the broker URL,
username, password, prefix, instance, client ID, keep-alive, self-signed toggle,
discovery toggle or the enable switch rebuilds the connection within about that
long. No restart.

**The interval is the exception, and it fails silently.** It is not part of what
the reconciler compares one poll to the next, and the tick length is read once,
when a connection is built. So lowering it from 10s to 2s saves cleanly, logs
nothing, shows no error — and the telemetry goes on ticking at 10s
indefinitely. To apply it, change one other field in the block at the same time
(toggling discovery off, saving, and toggling it back on is enough), or restart
polyemesis. The log line that confirms the new value is `mqtt telemetry
started`, which carries `everySeconds`.

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

A name that needs no changing is left alone, so `twitch` stays `twitch`. A
capital letter *is* a change: `Twitch` becomes `twitch-a731a58c`, and `Cam 1`
becomes `cam-1-4a3e9fe2`. Only a name that is already lowercase and already
`[a-z0-9_-]` survives unhashed — read the slug off the payload or off
`mosquitto_sub -t 'polyemesis/#'` rather than guessing it from the name.

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
>
> **This applies to the per-process figures only** — the source, destination and
> rendition payloads. `uptimeSec` on the host `state` topic is the polyemesis
> process's own uptime, measured from startup, and always has been. A host
> payload reading `uptimeSec: 3798` beside `sourcesLive: 0` is not a bug: it
> says the server has been up for an hour and nothing is on air.

There are four payload shapes. Every one of them carries `at`, the timestamp the
snapshot was taken, so a consumer can tell a fresh reading from a retained one it
was handed on subscribe.

```jsonc
// polyemesis/studio/state
{
  "version": "v0.9.0",       // `git describe --tags`, so v-prefixed and "v0.9.0-40-gb7b28b7a-dirty" off a tag; "dev" unstamped
  "startedAt": "2026-07-28T19:10:44Z",
  "uptimeSec": 3798.2,       // since the PROCESS started — see the note above
  "sources": 4,              // configured
  "sourcesLive": 3,          // actually receiving
  "destinations": 9, "destinationsUp": 8,
  "at": "2026-07-28T20:14:02Z"
}
```

`sources` and `sourcesLive` are a pair on purpose. "Nothing configured" and
"everything is off air" look identical from a single number and mean opposite
things.

```jsonc
// polyemesis/studio/source/cam-1-4a3e9fe2/state
{
  "id": 1, "name": "Cam 1", "slug": "cam-1-4a3e9fe2",
  "live": true,              // bytes arriving on the relay, not process state
  "ingestMode": "srt",
  "bitrateKbps": 6120.4,
  "uptimeSec": 3812.5,       // since media FIRST ARRIVED, not since spawn
  "restarts": 0,
  "lossPercent": 0,          // MPEG-TS continuity-counter loss
  "recording": true,
  "destinations": 3, "destinationsUp": 3,
  "failover": "backup",      // primary | backup | playlist | slate; omitted when the tier is off
  "ingestError": "…",        // omitted when empty; masked before it is captured
  "at": "2026-07-28T20:14:02Z"
}
```

`live` deserves a note: it is **bytes on the relay**, not process state. An SRT
or RTMP listener sits in "running" for as long as it waits for a publisher,
which is a different question from whether anything is arriving.

`failover` is the field to build an alert on and the easiest one to miss. It
names which input is currently on air — `primary`, `backup`, `playlist` or
`slate` — and it is `omitempty`, so it is absent from the JSON entirely when the
source has no failover tier running. Anything other than `primary` means you are
not broadcasting what you think you are broadcasting. A failover nobody notices
is how an operator finds out at the end of a broadcast that they streamed the
backup all night.

`ingestError` is FFmpeg's own last error line for the ingest, also `omitempty`.
It is masked before it is captured — see [What is deliberately not
published](#what-is-deliberately-not-published) — so it is safe to surface on a
dashboard, and it is usually the fastest answer to "why is `live` false".

```jsonc
// polyemesis/studio/source/cam-1-4a3e9fe2/dest/twitch-a731a58c/state
{
  "id": 7, "name": "Twitch", "slug": "twitch-a731a58c",
  "platform": "twitch",
  "kind": "rtmp",
  "enabled": true,
  "running": true,           // the ENGINE's verdict, not the process's
  "bitrateKbps": 6000.1,
  "uptimeSec": 3790.0,       // since media first arrived on this process
  "restarts": 0,
  "rendition": "1080p",      // the shared encode feeding it; omitted when none
  "error": "…",              // omitted when empty; masked
  "at": "2026-07-28T20:14:02Z"
}
```

`running` is the engine's verdict rather than the process's, and the difference
matters when you write the alert: a destination whose routing graph would not
compile has no process at all, and that is as down as a crashed one. The field
is `running`, not `live` — `live` belongs to sources only.

`kind` is one of `rtmp`, `srt`, `file` or `audio`. `platform` is one of
`custom`, `youtube`, `twitch`, `kick`, `facebook`, `rumble`, `trovo` or
`vimeo`, and is branding rather than transport — a `twitch` destination is an
`rtmp` one.

```jsonc
// polyemesis/studio/source/cam-1-4a3e9fe2/rendition/1080p/state
{
  "id": 3, "name": "1080p", "slug": "1080p",
  "consumers": 2,            // destinations sharing this encode
  "running": true,
  "width": 1920, "height": 1080, "fps": 30,
  "codec": "h264",
  "encoder": "libx264",
  "bitrateKbps": 6000.0,
  "error": "…",              // omitted when empty
  "at": "2026-07-28T20:14:02Z"
}
```

A rendition with `consumers: 0` has no process by design, so it reports
`running: false` and must not be alerted on as a failure. Check `consumers`
before you check `running`.

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

**Discovery is published on connect, and only on connect.** The payload goes out
the first time the broker link comes up and again after every reconnect — not on
every tick, because it describes the entity set and would otherwise be a
retained message rewritten every ten seconds for no reason. The consequence is
the part to know about: **a source or destination you add while the link is up
gets no Home Assistant entity until the link is rebuilt.** Its state topics
start publishing immediately — `mosquitto_sub` will show them within a tick —
so the symptom is a topic that plainly exists with no entity attached to it, and
Home Assistant is not the thing at fault.

To force the announcement, make the connection rebuild: change any field the
reconciler compares — broker URL, username, password, prefix, instance, client
ID, keep-alive, self-signed toggle, discovery toggle or the enable switch (the
discovery toggle itself, off and back on, is the least invasive) — or restart
polyemesis. **Nudging the publish interval will not do it**: the interval is not
part of that comparison, so the connection is never rebuilt and no entity
appears. Either way the next connect republishes the full device
payload, and the new entity appears.

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
with stable field names — all four shapes are written out under
[Payloads](#payloads), so a flow can be built against them without attaching
`mosquitto_sub` to a live broker to find out what a field is called. `status` is
a bare word rather than JSON specifically so an availability template needs no
`value_template`.

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

What a destination contributes is listed in full under
[Payloads](#payloads): identity (`id`, `name`, `slug`, `platform`, `kind`),
state (`enabled`, `running`, `rendition`), numbers (`bitrateKbps`, `uptimeSec`,
`restarts`) and a masked `error`. **No URL, stream key or passphrase has a field
to travel in** — the struct has none, and that absence is the mechanism, not an
omission from this page.

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

**I lowered the interval and the tick did not change.** Expected, and it is the
one setting that does not reconcile on its own. Change another MQTT field in the
same save, or restart. See [Setting it up](#setting-it-up) — the `mqtt telemetry
started` log line reports the interval actually in use, as `everySeconds`.

**A new source has no Home Assistant entity, but its topic is there.** Discovery
is published only when the broker link comes up. Force a reconnect by changing
any MQTT setting *except the publish interval* — the interval is not compared by
the reconciler, so changing it rebuilds nothing — or restart polyemesis. See
[Home Assistant](#home-assistant).

**TLS fails against a self-signed broker.** `mqtt connect failed` with a
certificate error means verification is doing its job. Either install the
broker's CA in the host trust store — the right fix — or turn on
`Accept a self-signed certificate`, which drops chain *and* hostname
verification for that connection. There is no middle setting.

---

## See also

- [MONITORING.md](MONITORING.md) — Prometheus, webhooks and the alert rules
- [SECURITY.md](../SECURITY.md) — how the broker password is stored
- [../docs/roadmap/MQTT.md](roadmap/MQTT.md) — the design, and what it got wrong
