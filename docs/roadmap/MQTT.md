# MQTT: retained telemetry, and alerts as a rider

**Status:** proposed, not started.
**Effort: 7–10 days**, or **5–6 days** for telemetry alone, which delivers most
of the value.
**Dependency: exactly one net-new module.**

---

## Recommendation

**Build retained telemetry first. Add MQTT alert delivery second, as a small
rider on the same connection.** They are not equal in value and not equal in
cost.

Alert-over-MQTT is largely redundant: `FormatJSON` already posts a structured
body to any URL, and every MQTT-shaped deployment — Home Assistant, Node-RED,
n8n — can consume a webhook and republish it.

**Retained telemetry has no existing path at all.** The event broker
(`internal/events`) is in-process and drops for slow subscribers by design; the
WebSocket needs a live browser. Nothing in polyemesis can currently answer *"is
the ingest up?"* to a consumer that was not connected when the state changed.

That is precisely what `retain=1` is for, and it is the only reason a Home
Assistant dashboard still shows the right thing after an HA restart.

## The dependency

**Take `github.com/eclipse/paho.golang`, pinned, using the `autopaho`
subpackage. Do not hand-roll, and do not take `paho.mqtt.golang`.**

Measured on this machine, not quoted: `paho.golang`'s build graph is **three
modules** — itself, `gorilla/websocket`, and `golang.org/x/net`. polyemesis
already has both of the latter (`gorilla/websocket` is direct, used by
`internal/api/ws.go`).

**Net-new modules: exactly one.**

That is a materially easier case than any other item on this roadmap, and it
clears the project's dependency bar comfortably.

> **Correction from verification — state this in the docs.**
> `paho.golang` is **MQTT 5.0 only.** Its own README: *"This client aims to
> implement the MQTT Version 5.0 Specification."* The original design never said
> so anywhere, while citing MQTT 3.1.1 throughout for retained-message, QoS and
> topic behaviour. The substance survives — those rules are identical in v5 —
> but the design must declare the protocol version it speaks, because a broker
> pinned to 3.1.1 will not talk to this client at all.

## Architecture

New package `internal/mqtt` owns the paho dependency. **`internal/alerts` must
never import paho** — it stays testable without a broker, exactly as it is
testable without a socket today via its `Doer` seam.

```go
// Publisher is the MQTT side, narrowed the same way Doer narrows HTTP so a
// test can decode wire bytes without a broker.
type Publisher interface {
    Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
    Connected() bool
}
```

Key configuration choices:

| field | value | why |
|---|---|---|
| `ClientID` | `polyemesis-<instanceID>` | collision is the number-one cause of mystery reconnect loops |
| `KeepAlive` | 30 | loss of connectivity is otherwise not detected |
| `WillMessage` | `polyemesis/<instance>/status` = `offline`, QoS 1, retained | the entire availability story |
| `ReconnectBackoff` | exponential 1 s → 30 s | mirrors the existing `Notifier` backoff |
| `ConnectPassword` | from `secrets.Box` | never from the URL |

### Offline buffering

For retained telemetry, buffering is **wrong**: a queued 90-second-old bitrate
replayed on reconnect is worse than none, because the next tick republishes
ground truth anyway. For alerts the buffer already exists one layer up in the
coalescer.

> **Correction from verification — this is a trap.** The original design set
> `Queue: nil` and documented it as "deliberate; no offline buffering". That is
> a **no-op**. `autopaho/auto.go:271` reads
> `if cfg.Queue == nil { cfg.Queue = memory.New() }` — autopaho silently
> substitutes an in-memory queue. Suppressing buffering requires an explicit
> no-op queue implementation, not a nil. Shipping the design as written would
> have produced exactly the stale-replay behaviour it argued against, while the
> comment claimed otherwise.

### Topic tree

```
polyemesis/<instance>/status                                retain, QoS 1  "online" | "offline"
polyemesis/<instance>/state                                 retain, QoS 1  host JSON
polyemesis/<instance>/source/<slug>/state                   retain, QoS 1  per-source JSON
polyemesis/<instance>/source/<slug>/dest/<slug>/state       retain, QoS 1  per-destination JSON
homeassistant/device/<instance>/config                      retain, QoS 1  HA discovery
```

**Everything is QoS 1, not QoS 0.** A conforming broker *may decline to store* a
retained QoS 0 message, and retained state that must survive a broker restart
therefore has to be QoS 1.

### Slugging is a correctness problem, not cosmetics

`Slug()` is the single chokepoint. Home Assistant's node/object id charset is
`[a-zA-Z0-9_-]`; MQTT topic names must contain no `+`, no `#`, no NUL.

- lowercase, `[a-z0-9_-]` kept, everything else → `-`, runs collapsed, trimmed;
- empty result → `x`;
- **append 4 hex of `sha256(original)` whenever the slug is not byte-identical
  to the input.** Two destinations named `Twitch (main)` and `Twitch [main]`
  must not collide — a collision silently overwrites one HA entity with another;
- assemble topics from a slice of already-slugged non-empty segments, never
  `strings.Join` over possibly-empty parts. A trailing `/` is a *distinct*
  topic, which would split telemetry into two streams no subscriber filter
  matches;
- reject a prefix beginning with `$`: a subscriber using `#` never receives
  `$`-prefixed topics, making the telemetry invisible in exactly the debugging
  scenario where you would go looking for it.

## Corrections carried from verification

Beyond the two already inline above:

- **The wildcard requirement ID is `MQTT-4.7.1-3`, not `-2`.** The substance is
  right; the identifier was wrong, and the design proposed writing these IDs
  into validation error messages.
- **Home Assistant does not "recommend" device discovery over per-component
  discovery.** Its docs present them as alternatives. Do not attribute a
  preference the source does not state.
- **The go-rtmp rejection lives in
  [DESIGN-ONE-PORT-ONLY.md](../DESIGN-ONE-PORT-ONLY.md), not
  `DEPENDENCIES.md`.** Cite the right file.
- **Line anchors into `internal/alerts` had drifted 30–60 lines**, and the
  design referred to a `Coalescer` type that does not exist — it is an
  unexported `coalescer`. Re-derive every anchor before implementing.
- **`engine.Status` carries no source *name*** and `Engine` exposes no accessor
  for one; only `SourceID()` and `Source()`. The topic tree needs a name, so
  that gap has to be closed first.
- **The "~120-line rider" and the 7–10 day estimate are unmeasured guesses** in
  a design that is otherwise explicit about what it measured. Treat them as
  looser than the rest.

## Test plan

- **Wire-level, no broker.** An in-test MQTT 5.0 server stub; assert exact
  topic strings, the retain flag, and QoS on every publish. Retain is the whole
  feature and it is one bit — assert the bit.
- **Slug collision.** Table test over adversarial names (`Twitch (main)` vs
  `Twitch [main]`, empty, all-punctuation, `$SYS`, 300 chars) asserting distinct
  topics.
- **Availability.** Kill the process without a clean shutdown and assert a
  subscriber receives the retained `offline` will message — the case the whole
  design exists for.
- **Restart survival.** Publish state, restart the *broker*, reconnect a fresh
  subscriber, assert it immediately receives current state. This is what QoS 1
  retained buys and what QoS 0 would silently fail.
- **No credential leaks.** Run the existing `redact` suite over every published
  payload; assert no stream key, token or password appears on any topic.
- **Acceptance** against a real `mosquitto` in CI.

## Risks

1. **Retained messages persist on the broker after polyemesis forgets them.** A
   renamed or deleted source leaves an orphan retained topic and a stale HA
   entity forever unless a zero-byte delete is published. The cleanup path must
   be designed, not bolted on.
2. **Credentials on a topic tree.** Redaction is not optional, and MQTT has no
   equivalent of the webhook URL masking already in place.
3. **MQTT 5.0 only** — see above.
4. **A second buffering layer** if the `Queue` trap is not handled explicitly.

---

## See also

- [ROADMAP](README.md)
- [../MONITORING.md](../MONITORING.md) — the existing Prometheus and webhook paths
- [../DEPENDENCIES.md](../DEPENDENCIES.md) — the bar this must clear
