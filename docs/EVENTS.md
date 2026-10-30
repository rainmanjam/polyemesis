# Real-time events API

**One long-lived HTTP response that tells a program what is happening, as it
happens.**

`GET /api/v1/events` is a [Server-Sent Events][sse] stream. Each event is a
small JSON body: a destination went down and why, the ingest started, the
programme failed over to its slate, a broadcast went live, the recorder
stopped, a chat message arrived, an alert fired. A browser reads it with
`EventSource`, and anything else reads it with a plain HTTP client. There is
no WebSocket handshake and no SDK.

[sse]: https://html.spec.whatwg.org/multipage/server-sent-events.html

## Events, webhooks, MQTT and `/ws`

| | Events API | [Webhooks](HOOKS.md) | [MQTT](MQTT.md) | `/ws` |
|---|---|---|---|---|
| Direction | you connect to polyemesis | polyemesis POSTs to you | both connect to a broker | you connect |
| Good for | a dashboard, a bot, a script that runs alongside | automation that must hear every event, even when it was not running | Home Assistant, retained current state | the web console |
| Missed while away | replayed for 5 minutes (`Last-Event-ID`) | retried, then counted as `missed` | the current state is retained | nothing |
| Body | the webhook envelope | the webhook envelope | per-topic payloads | console frames |
| Stable contract | yes, `specVersion` | yes, `specVersion` | yes | no, it is the UI's |

The events API uses **the same envelope as webhooks**. The fields have the same
names and meanings, and the same redaction applies. A script that reads
`.destination.name` and `.cause` from a webhook reads them the same way here.
There are three differences:

- The event's name is in `type`. A webhook calls it `trigger`.
- `data` carries the payload for the event families no webhook sends.
- The per-delivery fields (`sequence`, `missed`, `test`) are absent. Their job
  is done by the SSE `id` and replay.

## Connecting

```
GET /api/v1/events
GET /api/v1/events?topics=destination,ingest
```

**Authentication is the API's own.** Send either of these:

- an API token, as `Authorization: Bearer pmk_…`. This is what curl, Node and
  Python use.
- the console's session cookie. `EventSource` in a browser sends it on its own.

There is **no token query parameter**, and there will not be one. A credential
in a URL ends up in proxy logs, browser history and `Referer` headers, and the
only client that would need it is a browser, which already has the cookie.

**Scopes.** An `admin` token or a session receives every event. A `read` token
receives everything except:

- the `chat` topic. Chat is content viewers wrote under their names, and a read
  token is refused it everywhere else too.
- `alert` events that are not on the timeline. A read token is sent the
  stream-history alerts it can already read on `GET /timeline` (failovers,
  destinations going down, recovering, falling behind and catching up, and the
  ingest being lost or recovered). It is not sent the audit trail: sign-ins,
  token and settings changes, rule edits, debug exports and upgrades.

The rule lives in the event catalogue below, one row per type, and it fails
closed: a type nobody has classified reaches nobody.

**Revocation reaches an open stream.** Revoking the token, or changing the
password for a session, ends the stream within one heartbeat (15 seconds).
`EventSource` then reconnects, gets a `401`, and stops.

**Cross-origin requests are refused.** A request carrying an `Origin` header
for a different host gets a `403`, and no `Access-Control-Allow-Origin` header
is ever sent. Another site cannot hold a stream open on your cookie. Requests
with no `Origin` (curl, Node, Python) are not affected.

### Query parameters

| Parameter | Meaning |
|---|---|
| `topics` | Comma-separated, or repeated. Only these topics are sent. An unknown topic is a `400` that lists the valid ones, because a typo that silently subscribed to nothing would look exactly like a quiet install. |
| `lastEventId` | The same as the `Last-Event-ID` header, for a client that cannot set headers on its first request. It is an event id, not a credential. |

### Response

```
HTTP/1.1 200 OK
Content-Type: text/event-stream; charset=utf-8
Cache-Control: no-cache, no-transform
X-Accel-Buffering: no

retry: 3000
: polyemesis events

id: lq3k8c1w-41
event: destination.down
data: {"specVersion":"1","id":"lq3k8c1w-41","type":"destination.down","at":"2026-10-30T19:02:11Z","source":{"id":1,"name":"Main"},"destination":{"id":3,"name":"YouTube","platform":"youtube"},"reason":"stopped","error":"…","cause":"key_rejected","failure":{"cause":"key_rejected","line":"…","quick":true,"rejections":1,"at":"2026-10-30T19:02:01Z"}}

: heartbeat
```

- Each event is three lines: `id`, `event` (the type) and one `data` line of
  JSON. A blank line ends it.
- Lines starting with `:` are comments. A heartbeat comment is sent every 15
  seconds, which keeps proxies with a 60-second read timeout from cutting a
  quiet stream.
- `retry: 3000` asks `EventSource` to wait three seconds before reconnecting.

## Missed events, replay and gaps

Every event has an id, such as `lq3k8c1w-41`. The part before the dash names
this run of the server, and the number after it counts up by one per event.

When a client reconnects with `Last-Event-ID: <the last id it saw>`,
polyemesis first sends every event it still holds after that id, and then
carries on live. `EventSource` sends that header on its own. Events are held for
**5 minutes**, up to 4096 of them.

Sometimes polyemesis cannot prove it still has everything after the id. The id
may be older than what it holds, may come from before a restart, or may not
parse. In those cases it sends everything it does hold, preceded by a
`stream.gap` event. A `stream.gap` has no id. When you see one, re-read the
current state over REST (`GET /status`, `GET /timeline`).

### Slow clients are disconnected, not skipped

Each connection may fall **256 events** behind. Past that, polyemesis ends the
response instead of silently dropping events. A client that skipped one would
have no way to know. When `EventSource` reconnects with `Last-Event-ID`, the
replay fills the gap. A client that keeps being disconnected is not reading
fast enough. The server log says so: `events stream: disconnected a client that
fell behind`.

## The envelope

| Field | Type | Present |
|---|---|---|
| `specVersion` | string | always; `"1"` |
| `id` | string | always, except on `stream.gap`; equals the SSE `id` |
| `type` | string | always; one of the catalogue below |
| `at` | RFC 3339 time | always |
| `source` | `{id, name}` | when the event belongs to one programme. For alerts, the alert's own `key` and `title` name the subject. |
| `destination` | `{id, name, platform}` | on destination and broadcast events. Never a URL and never a stream key. |
| `reason` | string | why, in words. On `destination.down` it is `stopped`, `disabled`, `removed` or `stalled`, exactly as in webhooks. |
| `error` | string | the process's last error, redacted |
| `cause` | string | the classified failure: `key_rejected`, `platform_not_ready`, `dns`, `connection_refused`, `timeout`, `tls`, `codec_rejected`, `upload_too_slow`, `unknown` |
| `held` | bool | polyemesis stopped retrying; nothing happens until Retry |
| `keyChanged` | bool | the connected account reports a different stream key from the stored one |
| `failure` | object | the whole classified reason: `{cause, line?, quick?, held?, rejections?, keyChanged?, at}` |
| `data` | object | type-specific, below |

`cause`, `held` and `keyChanged` sit flat beside `reason`, as they do in a
webhook, so a jq one-liner reaches them without nesting. They are set only for
a failure: a destination that was switched off carries no leftover cause.

## Event catalogue

Every type below is emitted today. Each one is shown, by a test that drives its
real producer, to actually be emitted.

| Type | Topic | Read token | Meaning |
|---|---|---|---|
| `destination.up` | destination | yes | A destination started delivering. |
| `destination.down` | destination | yes | A destination stopped: failed, stalled, disabled or removed; reason says which, cause and failure say why. |
| `destination.held` | destination | yes | Polyemesis stopped retrying a destination after repeated quick rejections; nothing happens until Retry. |
| `destination.restarted` | destination | yes | A destination's process was respawned after it exited. |
| `destination.level` | destination | yes | A level (gain or mute) change reached a destination, applied live or by a restart. |
| `destination.rolledover` | destination | yes | A file destination continued into a new file because the configured one already held footage. |
| `ingest.published` | ingest | yes | Data started arriving on a programme's ingest. |
| `ingest.disconnected` | ingest | yes | Nothing has arrived on a programme's ingest for the disconnect window. |
| `failover.switched` | failover | yes | A programme switched between its primary, backup and slate sources. |
| `broadcast.live` | broadcast | yes | A platform broadcast was moved on air (or into testing) by polyemesis. |
| `broadcast.complete` | broadcast | yes | A platform broadcast was ended by polyemesis. |
| `broadcast.fault` | broadcast | yes | A platform refused to move a broadcast; the stream itself is unaffected. |
| `recording.started` | recording | yes | A programme's recorder started writing. |
| `recording.stopped` | recording | yes | A programme's recorder stopped writing; reason says why. |
| `chat.message` | chat | no | A chat message arrived from any connected platform. |
| `chat.retract` | chat | no | Chat messages that were delivered are gone: deleted, hidden, or swept by a ban. |
| `chat.moderation` | chat | no | A moderation action was taken, by an operator or by automod. |
| `chat.display` | overlay | no | A chat line cleared for on-screen display: judged by automod and stripped to what an overlay shows. |
| `chat.undisplay` | overlay | no | Chat lines that were cleared for display are gone and must leave the screen. |
| `alert` | alert | partly | An operator alert fired or resolved. |
| `stream.gap` | stream | yes | Events this connection asked to replay are no longer held; reconcile over REST. |

### Destinations

`destination.up`, `destination.down`, `destination.rolledover` and
`broadcast.fault` are the [webhook triggers](HOOKS.md#what-fires-and-when) of
the same names. They are derived once, by the same sweep and at the same moment,
and published to both. They follow the same dwells:

- `up` is immediate. It means the output is moving, not that a process was
  spawned.
- `down` comes after 10 seconds without delivery.
- A stall is a `down` with `reason: "stalled"`.
- A destination is not stalled because its source stopped. When the encoder
  disconnects, every destination's output stops; that is
  `ingest.disconnected`, said once, and no destination gets a `down`. This
  holds for every ingest mode. Before this fix an SRT source, which has no
  ingest process, sent a `destination.down` with `reason: "stalled"` for every
  running destination after each show, and never sent `ingest.published` or
  `ingest.disconnected`.

`destination.held` is announced once per hold. A hold usually lands inside the
10-second `down` dwell, so for a destination that was live the `held` follows
its `down` instead of arriving first. A destination that never went live has no
`down` coming, so its `held` is announced at once. After Retry, the next
`destination.up` is the recovery. A typical bad-key sequence is:

```
destination.down       cause=key_rejected  failure.rejections=1
destination.restarted  data.restarts=2
destination.held       cause=key_rejected  failure.rejections=3  held=true
      … the key is fixed and Retry is pressed …
destination.up
```

`destination.restarted` reports the supervisor respawning the same process:
`data: {"restarts": 9, "since": 2}`. A new process does not count. That covers
a settings change that rebuilt the destination, and Retry. The first time a
destination is seen it is not announced either, so a restarted server does not
report every destination's lifetime count.

`destination.level` says how a gain or mute change reached a destination.
`data: {"applied": "live"}` means it reached the running process without a
reconnect. `{"applied": "restart"}` comes with a `reason`, the one the
destination card shows. See [HOT-RELOAD.md](HOT-RELOAD.md).

### Ingest and failover

`ingest.published` and `ingest.disconnected` are the webhook triggers.

`failover.switched` carries `reason` (why it moved) and:

```json
{"active": "slate", "pinned": "", "switches": 3, "primaryLive": false,
 "backupLive": false, "backupEnabled": true, "slateEnabled": true}
```

### Go-live and stop

These come from the [broadcast lifecycle](SCHEDULED-BROADCAST.md):

- `broadcast.live` means polyemesis moved a connected platform's broadcast on
  air, or into testing.
- `broadcast.complete` means polyemesis ended one.

Both are sent only after the platform confirms the move. `data.phase` is the
platform's own word, never the phase that was requested:

```json
{"broadcastId": "dQw4…", "phase": "live", "redundant": false}
```

`redundant: true` means the platform said it was already there. The stream
itself going out or stopping is `ingest.published`/`ingest.disconnected` and
`destination.up`/`destination.down`.

### Recording

`recording.started` and `recording.stopped` mark the recorder process itself
starting and stopping. The recording setting alone does not count: with
recording enabled but the free-space guard holding, nothing is recorded and
nothing is announced. `reason` is one of:

- `started`
- `restarted to apply changed recording settings`, preceded by a
  `recording.stopped` for the same change
- `recording turned off`
- `stopped by the free-space guard: the recordings volume is nearly full`
- `the programme was stopped`

### Chat

The `chat` topic is withheld from read tokens. `chat.message` and
`chat.retract` carry the same payloads as the console's chat pane:

- A message is `{id, platform, account, channel, author, text, emotes, at, …}`.
- A retraction is `{platform, account, messageIds, authorId?, all?}`.

A message is delivered before automod has judged it. If automod or a moderator
removes it, a `chat.retract` follows, so a display fed from this stream should
apply retractions.

`chat.moderation` is a moderation action that succeeded. Failed or refused
actions are not announced.

```json
{"action": "timeout", "by": "automod", "platform": "twitch", "account": "…",
 "userId": "1234", "seconds": 600, "reason": "spam"}
```

`action` is one of `delete`, `hide`, `unhide`, `hide_local`, `ban`, `timeout`
or `unban`. `by` is `operator` or `automod`.

### Overlay

The `overlay` topic is the chat overlay's display projection (see
[OVERLAY.md](OVERLAY.md)). It is the only thing an overlay key can read, on
`GET /api/v1/overlay/events`; an admin session or token can also subscribe to
it here. Read tokens are refused it, as they are refused chat.

- `chat.display` is `{id, platform, name, color?, badges?, moderator?,
  subscriber?, broadcaster?, text, action?, at}`. There is no author id,
  account, channel or moderation field. `id` is opaque and only means something
  to a later `chat.undisplay`.
- `chat.undisplay` is `{platform, ids}`.

Unlike `chat.message`, a line is published only after automod has judged the
message. A message automod removes is never published here.

Replay treats a removed line differently for an overlay key: a `chat.display`
that a later `chat.undisplay` removed is never replayed to an overlay, whatever
`Last-Event-ID` it resumes from. An admin subscriber is replayed both events,
in the order they happened.

### Alerts

`alert` carries the operator alert exactly as a rule would deliver it, after the
alert redaction: `{type, severity, key, title, text?, fields?, at}`. `type` is
one of the [alert types](MONITORING.md), for example `destination.down` or
`destination.recovered`. Fired and resolved alerts are both `alert` events;
`data.type` says which.

Every alert is sent, whether or not a rule matches it. Coalescing is a
courtesy to a chat channel and does not apply here: a destination flapping 12
times is 12 `alert` events. A `read` token is sent only the alert types that
appear on `GET /timeline`; see [Connecting](#connecting).

### Redaction

All events go through the webhook redaction rules before they are numbered and
held for replay. A client connecting later is never replayed an unredacted copy.
The rules:

- free text (`reason`, `error`, names, `failure.line`) is scrubbed of URLs,
  keys and tokens.
- in `data`, a field whose name says credential is masked outright.
- an array of strings that would reveal a secret when joined (an argv) is
  masked as a whole.

## Examples

### curl

```sh
TOKEN=pmk_…   # Settings → API tokens; a read token is enough without chat
curl -N -H "Authorization: Bearer $TOKEN" \
  "https://stream.example.com/api/v1/events?topics=destination,ingest"
```

`-N` turns off curl's own output buffering. With the self-signed certificate,
pass `--cacert` with the CA from `GET /api/v1/tls/ca`; see [TLS.md](TLS.md).
To resume after a disconnect, add `-H "Last-Event-ID: lq3k8c1w-41"`.

### Browser (`EventSource`)

Same origin, signed in to the console. The cookie is sent on its own.

```js
const es = new EventSource("/api/v1/events?topics=destination,broadcast");

es.addEventListener("destination.down", (e) => {
  const ev = JSON.parse(e.data);
  console.log(`${ev.destination.name} is down: ${ev.cause ?? ev.reason}`);
});
es.addEventListener("destination.held", (e) => {
  const ev = JSON.parse(e.data);
  alert(`${ev.destination.name} is held (${ev.cause}); fix it and press Retry`);
});
es.addEventListener("stream.gap", () => refreshFromRest());
// EventSource reconnects by itself and sends Last-Event-ID.
```

### Node (18+, no dependencies)

```js
const res = await fetch("https://stream.example.com/api/v1/events", {
  headers: { Authorization: `Bearer ${process.env.TOKEN}` },
});
if (!res.ok) throw new Error(`HTTP ${res.status}`);

let buf = "";
const dec = new TextDecoder();
for await (const chunk of res.body) {
  buf += dec.decode(chunk, { stream: true });
  let end;
  while ((end = buf.indexOf("\n\n")) >= 0) {
    const block = buf.slice(0, end);
    buf = buf.slice(end + 2);
    const data = block.split("\n").find((l) => l.startsWith("data: "));
    if (!data) continue; // a comment: heartbeat
    const ev = JSON.parse(data.slice(6));
    console.log(ev.id, ev.type, ev.destination?.name ?? "");
  }
}
```

To resume, remember the last `ev.id` and send it as `Last-Event-ID` when you
reconnect.

### Python (requests)

```python
import json, os, requests

last = None
while True:
    headers = {"Authorization": f"Bearer {os.environ['TOKEN']}"}
    if last:
        headers["Last-Event-ID"] = last
    with requests.get("https://stream.example.com/api/v1/events",
                      headers=headers, stream=True, timeout=(10, 60)) as r:
        r.raise_for_status()
        for line in r.iter_lines(decode_unicode=True):
            if line.startswith("data: "):
                ev = json.loads(line[6:])
                last = ev.get("id") or last
                print(ev["type"], ev.get("cause", ""), ev.get("destination", {}).get("name", ""))
```

The 60-second read timeout is safe, because a heartbeat arrives every 15
seconds.

## Behind a reverse proxy

The response is unbuffered and never ends on its own. Two proxy settings
matter:

- **Buffering off.** polyemesis sends `X-Accel-Buffering: no`, which nginx
  honours. [`deploy/nginx.conf.example`](../deploy/nginx.conf.example) also
  sets `proxy_buffering off` for the whole site. Any other proxy needs its own
  equivalent; check that the first event arrives within a second of
  `curl -N` connecting through it.
- **A read timeout over 15 seconds.** The example's `proxy_read_timeout 3600s`
  is plenty.

With polyemesis terminating TLS itself (`tls.mode: auto`, `selfsigned`), the
stream works over HTTP/1.1 and HTTP/2 unchanged. The server sets no write
timeout, which would end every stream. Each write has its own deadline instead,
so a client that stops reading does not hold a connection open for ever.

## Coming with #14, #4 and #6

These are agreed but **not emitted yet**. The features that produce them do not
exist in this build. Each will be added to the catalogue by the feature that
introduces it, together with the test that proves it is emitted. Do not write a
consumer against them yet.

| Planned | Comes with | What it will carry |
|---|---|---|
| `poll.started`, `poll.vote`, `poll.ended` | #14 cross-platform chat polls | a polyemesis-run poll, its tallies and result |
| `viewers.sample` | #4 cross-platform analytics | the periodic per-platform viewer sample |
| a `degraded: …` reason on failover switches | #6 degraded-link failover | why a link was judged degraded |

## Known limitations

- **The replay window is 5 minutes and is held in memory.** A server restart
  forgets it. A client resuming across a restart gets a `stream.gap`.
- **Ids are per process.** Do not store them as durable keys. Use them only to
  resume.
- **An `alert` names its subject in its own `key` and `title`, not in
  `source`.** Audit alerts (sign-ins, token changes) belong to the install
  rather than to any programme.
- **`broadcast.live`/`broadcast.complete` need a connected account with the
  broadcast lifecycle enabled.** Without one, polyemesis never moves a
  platform's broadcast and there is nothing to announce.

## See also

- [HOOKS.md](HOOKS.md): the same envelope, pushed to you and signed.
- [API.md](API.md): authentication, tokens and scopes.
- [MONITORING.md](MONITORING.md): alert types and rules.
- [MONITORING.md](MONITORING.md#why-a-destination-is-failing): why a
  destination is failing, the retry hold and the key cross-check.
