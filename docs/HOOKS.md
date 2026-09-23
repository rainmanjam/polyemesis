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
| `destination.up` | a destination's output starts moving — not when its process spawns | none |
| `destination.down` | a destination stops — failed, disabled, deleted, or stalled with its process still running | 10s |
| `broadcast.fault` | a platform refused to start or end a broadcast | none |
| `destination.rolledover` | a file destination's recording continued into a different file | none |

"Delivering" is read from FFmpeg's own progress report: the destination's
output time has to advance between two sweeps, two seconds apart. A destination
pointed at an endpoint that refuses the connection therefore never sends
`destination.up`, however often its process is respawned. A sink that stops
taking data leaves the process running with its output frozen, and after the
10s dwell that is a `destination.down` with `reason: "stalled"`; when data moves
again, a fresh `destination.up`. While the ingest itself is disconnected every
destination's output stops, and that is reported once, as
`ingest.disconnected`, not as a `stalled` per destination.

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

**An empty `triggers` list means every trigger, not none.** It is the useful
default for the first hook somebody creates, and it is a trap on the second: an
operator who clears the list in the editor meaning "stop sending me things"
subscribes to the firehose instead. It also means the subscription grows
underneath you — `broadcast.fault` and `destination.rolledover` were both
appended to the table above after the fact, and every hook that had never
narrowed its `triggers` started receiving them on upgrade. If you want two
triggers, name two.

A trigger you mistype is refused at save time, with a `400` that names it:

```
hook "deploy" subscribes to "ingest.publish", which is not a trigger this build
fires; check the spelling against the trigger list, because a hook that keeps no
valid subscription at all means every trigger
```

A trigger that is already *stored* and unknown to the running build — a row
written by a newer release, or one whose trigger a later version removed — is
not refused on load. The hook keeps delivering the triggers this build does
fire, and the unknown name simply never matches anything. Refusing at load would
stop every hook on the install to punish one stale name.

## The envelope

```json
{
  "specVersion": "1",
  "id": "9f2c1a7b4e8d05c31f6a2b9047e1c8d3",
  "sequence": 42,
  "trigger": "destination.down",
  "at": "2026-07-31T18:04:11Z",
  "source": { "id": 1, "name": "Main" },
  "destination": { "id": 3, "name": "Twitch", "platform": "twitch" },
  "reason": "disabled"
}
```

`destination` is absent on the two ingest triggers. `reason` is free text meant
for a human reading a log; **branch on `trigger`, not on `reason`.**

`id` is **32 lowercase hex characters** — 16 bytes of `crypto/rand`, no prefix
and no separators — and it is what `X-Polyemesis-Delivery` carries. Size an
idempotency-key column for 32 characters, and do not validate it against a
narrower shape.

**Fields with nothing to say are absent, not empty.** `reason`, `error`,
`missed` and `test` are all omitted when they are empty or zero, so a healthy
delivery has no `error` key at all. Test for presence — in JavaScript
`body.error` is `undefined`, not `""` — rather than comparing against an empty
string.

`test` is the one field that is not about the pipeline:

```json
{
  "specVersion": "1",
  "id": "4c1f0b77a9e34d5280af61b3c7d9e025",
  "trigger": "ingest.published",
  "sequence": 0,
  "at": "2026-07-31T18:04:11Z",
  "test": true,
  "source": { "id": 0, "name": "test" },
  "reason": "test delivery from polyemesis"
}
```

It is `true` only on a delivery raised by the test button, and absent on
everything raised by something that actually happened. A test carries a real
trigger and a real signature, so **branching on `trigger` alone cannot tell a
test from a go-live**. A receiver that starts a recording or posts "we're live"
must check `test` first and refuse, or somebody clicking Test in the console
fires the show. The two supporting tells are `sequence: 0`, which no real
delivery ever has, and `source` being `{"id": 0, "name": "test"}`.

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

### When the *machine* loses the key

The secret is sealed with this install's key file, so a restore from backup onto
a different box, or a re-key, can leave polyemesis holding hook rows it cannot
open. **Such a hook stops delivering entirely. It does not fall back to sending
unsigned** — at the far end an unsigned delivery is indistinguishable from a
forgery, and that was the old behaviour, which was worse.

The condition surfaces as a `secretUnreadable` string on the hook's JSON, from
`GET /hooks` and `GET /hooks/{id}` (other fields omitted here):

```json
{
  "id": 4,
  "name": "deploy",
  "enabled": true,
  "secretUnreadable": "the signing secret could not be read on this machine — re-enter it to enable this hook"
}
```

The field is absent on every hook whose secret opened normally, which is all of
them on a healthy install. It is not a stored column: it is recomputed on every
read from whether the key file works *right now*, so restoring the right key
file clears it by itself with no repair step and nothing to un-set. The row is
deliberately left alone — `enabled` still reads `true` here, in the list and in
the database — so the fix is restoring one file rather than re-enabling every
hook by hand.

Know the shape of the symptom, because the usual place to look is no help: no
worker is started for such a hook, so **Recent deliveries stays empty**, which
this page otherwise tells you to read as a hook firing into a black hole. The
other signal is one Error-level log line per affected hook, repeated on every
five-second reload of the hook list:

```
a hook is not being delivered because its signing secret could not be read on
this machine; restore the key file or re-enter the secret. Nothing is being
sent unsigned.
```

Either restore the key file, or edit the hook and set a new secret.

## Ordering, retries and gaps

- **Ordering is per endpoint.** Deliveries to one hook arrive in the order the
  transitions happened. Nothing is ordered *across* hooks.
- **Three attempts by default**, 1–5. A **4xx is never retried** — an endpoint
  saying the request is wrong will say it again, and retrying only delays
  everything queued behind it. A 5xx, a 429 and a 408 are retried.
- **Ten seconds per attempt by default**, 1–30, set per hook as
  `timeoutSeconds`. Both it and `maxAttempts` are **clamped, not refused**: a
  hook saved with `"timeoutSeconds": 120` is stored as `30` and answers
  `201 Created` from `POST /hooks` (or `200 OK` from `PUT /hooks/{id}`) with no
  warning anywhere, and `0` or a missing value becomes the default.
  Read the value back from `GET /hooks/{id}` rather than trusting what you sent.
  The live bounds are served as `bounds` on `GET /hooks/meta`. Clamping rather
  than refusing is on purpose — a value that drifted out of range should cost a
  bounded timeout, not a hook that has silently stopped firing.
- **`sequence` counts from 1 per endpoint**, and it is assigned when the
  delivery *leaves* that endpoint's queue, not when the transition happened. So
  **a gap means a delivery was attempted and never accepted**: it exhausted
  `maxAttempts`, or your endpoint answered 4xx and it was abandoned on the first
  try. A gap does not mean an event was dropped — an event dropped before this
  point never consumes a number, so a drop leaves the sequence unbroken.
- **`missed`** is stamped on the next envelope *built* for that endpoint after a
  drop *at that endpoint's own queue*, saying how many were lost there. The
  counter is zeroed at that moment, before the first attempt is made: if that
  delivery then exhausts `maxAttempts` or is abandoned on a 4xx, the count is
  gone and appears on no later envelope, and all the receiver ever sees is a
  sequence gap. Go and reconcile; nothing reconciles for you.
- **`missed` does not see every drop.** There are two queues. The dispatcher has
  one 256-deep intake shared by every hook, and each endpoint has its own
  64-deep queue behind it. A drop at the per-endpoint queue bumps `missed`, and
  the receiver is told only if the next envelope built for it is accepted. A
  drop at the intake — the fan-out goroutine falling behind a burst —
  **bumps nothing that any receiver can see**: no `missed`, no
  sequence gap, and every endpoint carries on believing it is current. The only
  trace is `dropped` in the `stats` block of `GET /hooks/meta`, which counts
  both kinds together. A receiver that must not go quietly stale should
  reconcile on a timer as well as on `missed`.
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
  real bytes rather than against this page. The body carries `"test": true`; see
  [the envelope](#the-envelope) for why your receiver has to look at it.
- **A test never appears in Recent deliveries, and that is not a fault.** It is
  posted straight to the endpoint, skipping the queue and the subscription
  filter, so nothing records it. An empty Recent deliveries after a successful
  test is the expected result, not the black hole described next.
- **Recent deliveries** lists the last 50 per hook: trigger, sequence, status,
  duration and any error. A webhook that fires into a black hole is
  indistinguishable from one that does not fire at all, and for real transitions
  this is the difference.

The test button sends `ingest.published`. To exercise a different branch of your
receiver, name the trigger on the route:

```
POST /api/v1/hooks/4/test?trigger=destination.down
```

`?trigger=` accepts any name from the trigger table above, and the test ignores
the hook's subscription — a hook subscribed only to `ingest.published` still
delivers a `destination.down` test. `destination.up` and `destination.down` are
the two that attach a synthetic destination block,
`{"id": 0, "name": "Example destination", "platform": "custom"}`, which is the
only way to get a destination-shaped body without a destination changing state.

**An unrecognised trigger is silently replaced with `ingest.published`.** There
is no error and no warning: `?trigger=destination.dwon` answers `200` with a
perfectly valid `ingest.published` delivery, and an operator reading that result
concludes their `destination.down` routing works. Check the `trigger` field in
the body the console shows you against the one you asked for.

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

**Whether that happens depends on what else was already watching.** Three things
keep the sweep warm, and any one of them is enough: an alert rule exists, a hook
already exists, or a destination has a platform broadcast polyemesis starts and
ends for you — today that means YouTube, whose lifecycle coordinator consumes
the same edges the hook watcher does. If any of the three was already true the
sweep has been running, the watcher is warm, and adding a hook fires nothing. If
none of them was, it is cold and adding a hook fires the current state. So an
install with no alert rules but one lifecycle-managed YouTube destination is
warm, and reasoning from alert rules alone will predict the wrong answer there.
That inconsistency is real and is not fixed here.

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
