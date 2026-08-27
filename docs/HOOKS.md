# Lifecycle webhooks

**One signed POST per transition, in order, for a script rather than a person.**

When the stream starts, when it stops, when a destination goes live or drops,
polyemesis POSTs a small JSON body to a URL you own. Set them up under
**Automation → Webhooks**.

## Hooks are not alerts

Both post to a URL. They promise opposite things, and choosing the wrong one is
the most likely way to be disappointed.

| | Alert rule | Webhook |
|---|---|---|
| Audience | a person reading Slack | a script |
| Repeats | **coalesced** — "×12" | **never** — one delivery per transition |
| Delay | debounced 10s, rate-floored 30s | none beyond the dwell below |
| Ordering | none | **guaranteed per endpoint** |
| Signed | no | **HMAC-SHA256** |
| Body | formatted for the channel | stable, versioned, machine-readable |

Coalescing an alert is a kindness. Coalescing a hook loses the eleven events the
script needed.

## What fires, and when

| Trigger | Fires when | Dwell |
|---|---|---|
| `ingest.published` | data arrives on the ingest after silence | **none** |
| `ingest.disconnected` | nothing has arrived for 5s | 5s |
| `destination.up` | a destination starts delivering | none |
| `destination.down` | a destination stops — failed, disabled or deleted | 10s |
| `broadcast.fault` | a platform refused to start or end a broadcast | none |
| `destination.rolledover` | a file destination's recording continued into a different file | none |

`destination.rolledover` is also **not** a `destination.down`. Nothing stopped:
the destination is delivering and the recording is continuing. What changed is
which file it is continuing into.

A file destination never overwrites footage. If its child exits and is respawned
while the configured filename already holds real bytes, the replacement is given
a timestamped sibling — `show.mkv` becomes `show-20260819-021500.mkv` — so the
earlier take survives. That is the intended behaviour and it is not going to
change.

What was missing is anyone being told. The respawn is not an error: the child
can exit cleanly, and a clean exit is logged at Info with nothing in the process
log ring, so the only trace was a restart counter moving. An operator who looked
at the filename they configured found a file with a header and no video, and no
reason anywhere to suspect a sibling existed. `reason` carries the path actually
written, so a script can follow the recording rather than guess at it.

`broadcast.fault` is **not** a `destination.down` and must not be treated as
one. The stream is fine: bytes are flowing and the destination is delivering.
What failed is the platform's own idea of the broadcast — the channel is at its
concurrent-broadcast limit, the broadcast has already been completed and cannot
return to live, the connected account's token expired. polyemesis never stops a
stream because a transition failed, so a script that mirrors "what are we live
to" must not tear anything down when it hears this. The `reason` field carries
the operator-facing sentence and the broadcast id.

`ingest.published` has **no dwell on purpose**. An operator scripting "we are
live" wants it now, and the cost is stated below under limitations.

The dwell on `destination.down` is what stops a reconnecting destination
producing a storm: it never goes down inside the window, so it never comes back
up either.

`ingest.disconnected` can only fire **after** a publish, so a server sitting idle
since boot never announces a disconnection that never happened.

## The envelope

```json
{
  "specVersion": "1",
  "id": "d_9f2c1a7b4e",
  "sequence": 42,
  "trigger": "destination.down",
  "at": "2026-07-31T18:04:11Z",
  "source": { "id": 1, "name": "Main" },
  "destination": { "id": 3, "name": "Twitch", "platform": "twitch" },
  "reason": "disabled",
  "error": ""
}
```

`destination` is absent on the two ingest triggers. `reason` is free text meant
for a human reading a log; **branch on `trigger`, not on `reason`.**

Headers:

| Header | Contents |
|---|---|
| `X-Polyemesis-Signature` | `v1=` + hex HMAC-SHA256 |
| `X-Polyemesis-Timestamp` | Unix seconds, covered by the signature |
| `X-Polyemesis-Trigger` | the trigger, for routing without parsing |
| `X-Polyemesis-Delivery` | unique per delivery; the idempotency key |
| `X-Polyemesis-Sequence` | per-endpoint counter, for spotting gaps |

## Verifying a signature

The signature is over `"<timestamp>.<raw body>"`. **Use the raw bytes**, not a
re-serialised object — any difference in key order or spacing changes the digest.

```js
const crypto = require("crypto");

// express.raw({ type: "application/json" }) — req.body must be a Buffer.
function verify(req, secret) {
  const ts = req.get("X-Polyemesis-Timestamp");
  const sig = req.get("X-Polyemesis-Signature");
  // Reject anything older than five minutes, or a captured delivery can be
  // replayed forever.
  if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return false;

  const mac = crypto.createHmac("sha256", secret);
  mac.update(ts + "." );
  mac.update(req.body);
  const want = "v1=" + mac.digest("hex");
  return crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(want));
}
```

```python
import hmac, hashlib, time

def verify(headers, raw_body: bytes, secret: str) -> bool:
    ts = headers["X-Polyemesis-Timestamp"]
    sig = headers["X-Polyemesis-Signature"]
    if abs(time.time() - int(ts)) > 300:
        return False
    mac = hmac.new(secret.encode(), digestmod=hashlib.sha256)
    mac.update(ts.encode() + b".")
    mac.update(raw_body)
    return hmac.compare_digest(sig, "v1=" + mac.hexdigest())
```

The signing key is shown **once**, when the hook is created. polyemesis stores
it sealed and cannot show it again — if you lose it, edit the hook and set a new
one.

## Ordering, retries and gaps

- **Ordering is per endpoint.** Deliveries to one hook arrive in the order the
  transitions happened. Nothing is ordered *across* hooks.
- **Three attempts by default**, 1–5. A **4xx is never retried** — an endpoint
  saying the request is wrong will say it again, and retrying only delays
  everything queued behind it. A 5xx, a 429 and a 408 are retried.
- **`sequence` counts from 1 per endpoint.** A gap means deliveries were
  dropped.
- **`missed`** appears on the next successful envelope after a drop, saying how
  many were lost. Go and reconcile; nothing reconciles for you.
- **`sequence` resetting to 1 means polyemesis restarted.** See the limitations.

## What is deliberately not in a payload

No destination URL, no stream key, no publish token, no ingest passphrase.

This is enforced centrally rather than per call site: the free-text fields go
through the same redaction the alert path uses, on the way in. It matters more
than it looks — `error` carries the last lines of FFmpeg's stderr, and an FFmpeg
that cannot publish prints the whole `rtmps://` URL with the key on the end.

Pinned by [internal/hooks/payload_test.go](../internal/hooks/payload_test.go),
which plants a key in three fields and fails if any of them reaches the wire,
plus a structural guard that walks the marshalled JSON so a field added later
cannot smuggle a credential out by being named after one.

## Testing without going live

- **The test button** sends a real signed delivery and shows you the exact body
  and signature that were sent — so you can check your verification code against
  real bytes rather than against this page.
- **Recent deliveries** lists the last 50 per hook: trigger, sequence, status,
  duration and any error. A webhook that fires into a black hole is
  indistinguishable from one that does not fire at all, and this is the
  difference.

## Private and LAN endpoints: `allowPrivateTarget`

A hook URL that resolves to a non-public address is **refused at save time**, and
the create or update answers `400` naming the hook:

```
hook "lan-collector" targets a non-public address; set allowPrivateTarget to
permit a self-hosted endpoint on purpose
```

Refused without the opt-in: loopback, link-local — including the cloud metadata
address `169.254.169.254` — RFC1918 (`10/8`, `172.16/12`, `192.168/16`),
RFC6598 shared address space — carrier NAT, and the range Tailscale hands out —
IPv6 unique-local, and the IPv6 equivalents of the same. A
hook anyone with console access can point at the metadata service is a pivot out
of the operator console into the rest of the network, not a feature.

Plenty of people run a collector on their own LAN on purpose, so there is an
opt-in rather than a flat refusal — a refusal with no escape hatch just gets the
whole feature turned off. Send `allowPrivateTarget` on the create or the update:

```json
{
  "name": "lan-collector",
  "url": "http://192.168.1.20:9000/polyemesis",
  "triggers": ["ingest.published", "ingest.disconnected"],
  "allowPrivateTarget": true
}
```

It defaults to `false`, it is stored per hook, and it is returned by `GET /hooks`
alongside the other fields.

**The save-time check is not the enforcement.** A hostname is re-resolved and
re-checked at dial time on every delivery, which is what closes DNS rebinding —
a name that answered publicly when the hook was saved and answers `127.0.0.1`
afterwards is refused then, too. The save-time check exists in addition so an
obviously bad hook is rejected while somebody is looking at the form, rather
than three retries into a delivery attempt.

## Known limitations

Written out rather than discovered later.

**A restarted server replays the current state as fresh events.** The ingest and
every destination start in the OFF position, so a process restarted mid-broadcast
fires `ingest.published` and one `destination.up` per live destination within two
seconds. This is deliberate — the alternative is a script that never runs because
the transition happened while the server was down — but it means **every receiver
must be idempotent**. `sequence` resetting to 1 is the signal.

**Adding the first hook mid-broadcast does the same.** The sweep does not run
when nothing is subscribed, so the watcher's first observation is whatever is
true at that moment.

**Whether that happens depends on your alert rules.** If alert rules already
exist the sweep has been running, the hook watcher is warm, and adding a hook
fires nothing. If they do not, it is cold and adding a hook fires the current
state. That inconsistency is real and is not fixed here.

**A one-sample handshake blip produces a publish/disconnect pair.**
`ingest.published` has zero dwell by design. An SRT connection that delivers a
few bytes and dies will announce a stream that never happened, followed five
seconds later by a disconnection.

**Dropped deliveries are counted, not recovered.** There is no persistent
outbox, no at-least-once guarantee, and no ordering across a restart.

**Retries block their own endpoint.** Ordering is bought with head-of-line
blocking, bounded at `maxAttempts × (timeoutSeconds + backoff)` — 33s at
defaults, 165s at the maximums. A slow endpoint delays only its own deliveries,
but it delays all of them.

**SSRF is defended for hooks, and not yet for alert webhooks.** A hook URL
pointing at loopback, a cloud metadata service or a LAN address is refused at
save time and again at dial time, and reaching one on purpose needs
`allowPrivateTarget` — see [above](#private-and-lan-endpoints-allowprivatetarget).
An **alert rule's** webhook is still in the old position: it is not checked, so
anyone who can create one can still POST from this server to `127.0.0.1` or
`169.254.169.254`. Anyone who can do either can already reconfigure the
pipeline, which is why it is bounded — but the two paths are not level yet.

**No per-source subscription filter.** A hook receives from every source. An
install with three programmes gets three `ingest.published` events,
distinguishable by `source.id`.

**No separate enable/disable triggers.** A deliberate disable arrives as
`destination.down` with `reason: "disabled"` — free text, and therefore weaker
than a trigger name.

**The delivery log is in memory and per process.** Fifty entries per hook, gone
on restart. A debugging aid, not an audit trail.

**The engine wiring is only shallowly tested.** The gate that decides whether a
sweep runs has a unit test and a verified mutation, but "the snapshot actually
reaches the hook watcher" is proved by nothing except the build. An end-to-end
test would need a live relay hub and a real FFmpeg, which this repository does
not do for the alert path either.

**`hooks` importing `alerts` is a real coupling.** The watcher takes an
`alerts.Snapshot` and the payload uses the alert package's redaction. If
`alerts.Snapshot` gains a field carrying a URL, the hook payload inherits the
risk: the structural guard catches a field *named* after a secret, not one
merely *containing* one.

## See also

- [MONITORING.md](MONITORING.md) — alerts, for when a person needs telling
- [MQTT.md](MQTT.md) — retained state telemetry, for a dashboard
- [API.md](API.md) — the `/hooks` routes
