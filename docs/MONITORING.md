# Monitoring

Everything the dashboard draws is also available as Prometheus metrics, so the
things you would otherwise learn by looking at a screen — a destination
flapping, the recording disk filling, nobody actually streaming — can page you
instead.

- [The metrics endpoint](#the-metrics-endpoint)
- [Why it requires authentication](#why-it-requires-authentication)
- [Metric conventions](#metric-conventions)
- [Queries to start from](#queries-to-start-from)
- [Built-in alerts](#built-in-alerts)
- [Automation](#automation)

Prometheus is not the only way out. polyemesis also publishes retained state to
an MQTT broker, with Home Assistant discovery, which suits a dashboard you
already run better than a scrape target does — see [MQTT.md](MQTT.md).

---

## The metrics endpoint

`GET /api/v1/metrics` returns Prometheus text exposition covering ingest state
and bitrate, per-destination state, bitrate, restarts and dropped frames, relay
throughput and drops, recording disk usage, and the process's own CPU and
memory.

**The whole host is covered too**, which is worth knowing before you install
node_exporter beside a server that already exports it:
`polyemesis_host_cpu_percent`, `polyemesis_host_cpus`,
`polyemesis_host_memory_used_bytes` and `polyemesis_host_memory_total_bytes` are
machine-wide numbers, not the process's. Three scalars round the exposition out:
`polyemesis_build_info`, always 1 and carrying a `version` label to join onto a
dashboard or to alert on after a staged upgrade went somewhere unexpected;
`polyemesis_uptime_seconds`; and `polyemesis_recording_files`, the segment count
beside the byte counts. The alert rules' own delivery outcome is there too —
see [When the alerts themselves stop arriving](#when-the-alerts-themselves-stop-arriving).

**The endpoint requires authentication.** It accepts an API token, which is what
a scraper should use — create one under *Settings → API tokens* and point
Prometheus at it:

```yaml
scrape_configs:
  - job_name: polyemesis
    scheme: https                       # NOT optional -- see below
    metrics_path: /api/v1/metrics
    static_configs:
      - targets: ['stream.example.com:443']
    authorization:
      credentials_file: /etc/prometheus/polyemesis.token
    tls_config:
      # The install's local CA, for tls.mode selfsigned (install.sh's default).
      # Copy <dataDir>/tls/ca.crt to the Prometheus host -- TLS.md says how.
      # Delete this line for an ACME or manual certificate a browser trusts.
      ca_file: /etc/prometheus/polyemesis-ca.crt
```

**Say `scheme: https`, and name the port.** Prometheus defaults to `http` on
port 80, and on an install that terminates TLS itself port 80 is the
HTTP→HTTPS redirect. So a scrape configured without a scheme sends its
`Authorization` header — the token — **in cleartext, once per scrape**, and
only then is redirected. Nothing on the server can prevent that, because the
header is already on the wire by the time the redirect answers; the fix has to
be in the scrape config. `:443` is `install.sh`'s default. Use `:8080` if you
kept that port, and `scheme: http` with `:8080` only for a plain-HTTP install
reached over loopback or a private network — and then the token is in the clear
by design.

`tls_config.ca_file` is what lets a self-signed install verify at all:
without it the scrape fails certificate verification, and the tempting fix,
`insecure_skip_verify: true`, sends the token to whoever answers. The
self-signed leaf names `tls.hostname`, `localhost` and the loopback addresses,
so scrape by that hostname — or set `tls_config.server_name` to it when the
target is an IP.

A session cookie works too, so you can just open the URL in a signed-in browser
tab while you are working out what to graph.

## Why it requires authentication

Many projects leave `/metrics` open to loopback. Here, loopback is both too
strict and too lax.

Prometheus normally runs in a neighbouring container, so its scrape arrives from
a bridge address and would be refused. And once `trustProxyHeaders` is on,
*every* request arrives from a proxy on `127.0.0.1`, so the same check would let
the whole internet in.

A revocable token is correct in both deployments, and revoking it does not
require restarting the server.

## Metric conventions

- Names carry the `polyemesis_` prefix.
- Counters end in `_total`.
- Values are in base units — bytes, seconds, bits per second. **The two CPU
  gauges are the exception, and they do not share a denominator.**
  `polyemesis_process_cpu_percent` is a percentage of *one core*, so it
  legitimately reads 180 on a box where FFmpeg has two of them busy;
  `polyemesis_host_cpu_percent` is a percentage of the whole host. A `> 90`
  rule on the first one pages you for normal operation.
- Destinations are labelled `id` and `name`. **So are ingest and relay series**,
  where the pair names the *programme* rather than a destination: one install
  can run several, and `polyemesis_ingest_up`, `polyemesis_ingest_state`,
  `polyemesis_ingest_bitrate_bits_per_second`, `polyemesis_ingest_restarts_total`
  and all five `polyemesis_relay_*` families carry one series per programme.
  **Every programme in the sources table has these series, including one the
  server has nothing running for**: a programme whose engine failed to build or
  start is reported as a stopped ingest, and `polyemesis_source_engine_up` is 0
  for it and 1 for every programme that has an engine.
- `polyemesis_destination_info` carries `kind`, `platform` and `source_id` for
  joining. `source_id` is the `id` of the programme's ingest series, so it is how
  a destination query asks about its own programme's ingest (see the slow-output
  query below). It is empty for a destination that belongs to no programme.
- `polyemesis_ingest_state` and `polyemesis_destination_state` carry a `state`
  label, and emit **every** state for **every** process — `stopped`, `starting`,
  `running`, `reconnecting`, `failed` — with the four a process is not in
  sitting at 0. The zeroes are deliberate, not stale: a series that blinks into
  existence only once the condition has happened is a series an alert cannot be
  written against before the first outage. It is what makes the most useful
  query on this endpoint writable at all —
  `polyemesis_destination_state{state="reconnecting"} == 1` catches a
  destination flapping well before the twenty seconds it takes to be judged
  down.

The family headers are emitted even on an install with nothing configured, for
the same reason: a `# TYPE polyemesis_ingest_up gauge` with no samples under it
is what lets you write the alert before the first source exists.

## Queries to start from

```promql
polyemesis_ingest_bitrate_bits_per_second == 0
  and on() polyemesis_sources > 0                           # nobody is streaming
polyemesis_destination_up == 0 and polyemesis_destination_enabled == 1
rate(polyemesis_destination_restarts_total[15m]) > 0        # a flapping output
polyemesis_destination_stalled == 1                         # connected, not delivering
rate(polyemesis_destination_output_seconds_total[1m]) < 0.9
  and polyemesis_destination_up == 1
  and on(id) (polyemesis_destination_info
    and on(source_id) label_replace(
      min_over_time(polyemesis_ingest_bitrate_bits_per_second[1m]) > 0,
      "source_id", "$1", "id", "(.*)"))                     # moving, but slowly
polyemesis_recording_free_bytes < 20e9                      # disk filling up
polyemesis_source_engine_up == 0                            # a programme the server is not running
```

**The last is never normal.** `polyemesis_ingest_up` is 0 for a listener
waiting on its streamer too, which is ordinary between shows.
`polyemesis_source_engine_up` is 0 only when a configured programme has no
engine at all: its ingest failed to build or to start, the server logged it and
kept the other programmes on air. Nothing is receiving that programme's
stream and no built-in alert can fire for it, because the alert watcher runs
inside the engine. `GET /api/v1/health` reports the same state as `degraded`
with a `200`, and its `engine` check says `N of M source(s) running`.

The second is the one worth alerting on first: a destination that is enabled but
not up is a platform you think you are streaming to and are not.

**`_up` means delivering, not just running.** A platform that stops taking data
leaves the FFmpeg process running and connected, with its output frozen. That
destination reads `polyemesis_destination_up` 0 and
`polyemesis_destination_stalled` 1 once its output has not advanced for
5 seconds. `polyemesis_destination_state{state="running"}` stays 1, because the
process is still running, and restarts stay flat.
`polyemesis_destination_bitrate_bits_per_second` reads 0 while the stall lasts.
Otherwise it is FFmpeg's average over the whole run, so it only ever falls
slowly. The same stall shows in `/api/v1/status`: the destination has
`"stalled": true`, its `process` has `"stalled": true` and `stalledSec`,
`progress.bitrateKbps` and `progress.speed` read 0, and `warnings` says it is
stalled. A destination that
has not moved any media yet, for example one still in its probe window, is not
stalled.

**A lost ingest is not a destination stalling.** While the ingest is lost,
every destination's output stops because there is nothing to send. That is
`polyemesis_ingest_bitrate_bits_per_second` 0 for that programme, said once.
It is not `polyemesis_ingest_up` 0: `_up` says the ingest process is running,
and an SRT or RTMP listener goes on running while it waits for a publisher
that has gone away. A destination stays `_up` 1 and
`_stalled` 0 through it, and it has no warning, so the alert above does not
fire once per destination for one missing source. The process itself still
reports `"stalled": true` in `/api/v1/status`, and its bitrate reads 0, because
both are true of the process. The destination's own `"stalled"` field, which
`_up`, `_stalled` and the warning follow, stays false. This is the same rule
the `destination.down` hook and the `falling_behind` alert apply. When the
source comes back, a destination whose output has not resumed is counted as
stalled only once the source has been arriving for those 5 seconds, so a
returning ingest does not flash every destination stalled while each one
catches up.

`polyemesis_destination_output_seconds_total` is the media time delivered, so
its `rate()` is about 1 while a destination keeps up, below 1 while it is falling
behind without stopping, and 0 while it is stuck. The fifth query uses it,
with two guards so that it means *this destination* is moving but slowly.
`_up == 1` leaves out every disabled or stopped destination, whose rate is 0,
and every stall that the stall query already reports. The join leaves out every
destination whose programme's ingest has not been arriving for the whole last
minute. Without it, a lost ingest would fire the query once per destination,
because `_up` stays 1 through a lost ingest by design. The same happens for the
minute after the ingest returns, while `rate()` still averages over the gap.
The `[1m]` in `min_over_time` matches the `[1m]` in `rate()`; if you widen one,
widen both. `label_replace` renames the ingest's `id` to `source_id`, because
destination and ingest series both have `id`, but for different things.

`polyemesis_destination_output_bytes_total` is the same counter in bytes, and `rate()`
of it times 8 is the bitrate actually being sent now. Both reset with the
process, which `rate()` handles.

**The first watches the bitrate, not `polyemesis_ingest_up`.** `_up` is 1 while
the ingest process is running, and an SRT or RTMP listener runs the whole time
it waits for OBS. A publisher that disconnects leaves it at 1, so
`polyemesis_ingest_up == 0` never fires for the most common way a stream ends.
The bitrate is what arrives at the relay, sampled every second, and it reads 0
when nothing does.

**The first also needs its guard.** A bare bitrate `== 0` cannot tell a
broadcast that ended from an install nobody has configured yet — every series
here reads zero in both cases — which is what `polyemesis_sources`, the count of
programmes on this install, is for. That series is *omitted* rather than
published as 0 when the count cannot be read, because for this one number 0 is
not a smaller version of the truth but its opposite: a fabricated zero would
silence the outage alert and fire "nobody has configured this server" at a box
that is on air. Use `absent(polyemesis_sources)` if you want to know about that
case. And because ingest series are per programme, this query fires once per
idle source rather than once per install — on a multi-programme box, read the
`name` label before concluding the whole server is dark.

**The disk query's `20e9` is this page's number, not the product's.** The
built-in `disk.low` alert below trips at 2 GiB free *or* under 5% of the volume,
an order of magnitude lower, so on a large recordings volume Prometheus pages
days before the in-process alert does. They are two separate thresholds, not two
views of one; pick the path you want to be woken by, or set them to agree.

## Built-in alerts

If you do not want to run Prometheus, polyemesis has its own alert rules with
webhook delivery — the same conditions, evaluated in-process, posted to a URL
you supply. Configure them under *Automation → Alerts*, or through
`/api/v1/alerts` ([API.md](API.md#alerts-and-schedules)).

Webhook URLs often carry their credential in the path, so they are masked in
every API response. Handing the masked form back on an update means "unchanged".

### Setting a rule up

A rule is an endpoint, a subscription and a few knobs. The knobs are where the
surprises are, so they are worth setting deliberately rather than taking the
defaults and then wondering:

| Field | Default | Range | What it does |
|---|---|---|---|
| `format` | `json` | `json`, `discord`, `slack` | The body shape. The default is the generic envelope described below — **a Discord or Slack webhook URL with the format left alone gets a body that service rejects**, and the failure reads as a broken endpoint rather than a wrong field |
| `debounceSeconds` | `10` | 1–3600 | How long an event waits for company. Everything raised about the same subject inside the window becomes one message carrying a count, so a destination flapping every two seconds is one alert saying "12 times" rather than twelve alerts |
| `minIntervalSeconds` | `30` | 1–86400 | The floor between two deliveries to this endpoint. Events raised while it is in force are **not** dropped — they keep coalescing and ride out on the next delivery |
| `minSeverity` | `info` | `info`, `warning`, `critical` | Drops anything quieter |
| `events` | empty | any type on this page | Empty means **every** type |
| `allowPrivateTarget` | `false` | — | The deliberate opt-in past the SSRF guard; see below |

A debounce or interval out of range is **clamped rather than refused**, because
a value that drifted out of bounds should cost a bounded message rate and not a
rule that has silently stopped alerting. An unknown `format` or `minSeverity` is
refused at save, and so is an unknown event name — by name, so that a typo in
`events` cannot quietly become the empty list that means "everything".

Delivery is retried. `settings.alerts.retryAttempts` — **install-wide, not
per-rule** — is **4 by default and accepts 1 to 10**, first try included, with
each attempt given 10 seconds and the wait between them doubling from 1 second
to a 30-second ceiling. A `429` carrying `Retry-After` is honoured as sent,
capped at 60 seconds so a mistaken value cannot park the sender for an hour. A
`408 Request Timeout` is retried too, on the ordinary backoff and through the
whole budget, because a timeout says nothing about whether the request was
right. Any other 4xx is the endpoint saying the request itself is wrong and is
not retried at all. Raising the number is how you tolerate an endpoint that is
down rather than slow; the backoff curve underneath is not exposed, and a saved
change is applied to the running notifier of every programme without a restart.

#### When the alerts themselves stop arriving

An endpoint that refuses every delivery (a rotated Slack URL, a deleted
Discord channel) cannot tell you so through itself. The *Automation → Alerts*
page shows the failure count, but only to somebody who opens it. The scrape
carries it too, summed over every programme:

- `polyemesis_alert_deliveries_total{result="sent"}` and `{result="failed"}`
  count deliveries, not events: one delivery carries everything coalesced into
  it, and a failure is counted once the retry budget above is spent. A
  deleted programme's deliveries stay in the total, so removing one never
  lowers it; the count starts again from 0 only when the server process
  restarts, which `increase()` and `rate()` treat as a counter reset.
- `polyemesis_alert_last_success_timestamp_seconds` is the Unix time of the
  newest delivery that succeeded. It is **absent** until one has, not 0: a 0
  would make every quiet install look decades overdue.

The alert to write on it is the failure, not the silence, because a quiet
night sends nothing either:

```promql
increase(polyemesis_alert_deliveries_total{result="failed"}[30m]) > 0
  and increase(polyemesis_alert_deliveries_total{result="sent"}[30m]) == 0
```

That is "deliveries are being attempted and none is getting through". Route it
somewhere other than the webhooks it is about.

#### A receiver on your own network

The SSRF guard refuses a rule whose URL points at a non-public address, and it
is two checks rather than one. A literal private or loopback IP is refused at
save with a 400:

```
alert rule "Home ntfy" targets a non-public address; set allowPrivateTarget to
permit a self-hosted endpoint on purpose
```

A *hostname* is deliberately not resolved at save — DNS from inside a save
request is slow, flaky offline, and its answer can legitimately change before
the alert is ever delivered — so it is checked at dial time instead, which is
the one point a DNS answer that changed after the save cannot lie to. A rule
naming `ntfy.lan` therefore saves cleanly and then fails to deliver.

Both are lifted by the same field, and **it has to travel in the create or
update body that carries the URL**, because the save that stores the URL is the
save that validates it:

```sh
curl -H "Authorization: Bearer pmk_..." -H "Content-Type: application/json" \
     -X POST https://stream.example.com/api/v1/alerts/rules \
     -d '{
       "name": "Home ntfy",
       "url": "http://192.168.1.20:8080/polyemesis",
       "format": "json",
       "minSeverity": "warning",
       "allowPrivateTarget": true
     }'
```

On an update — `PUT /api/v1/alerts/rules/{id}`, whose body is PATCH-shaped:
every field is a pointer and an omitted one leaves the stored value alone —
leaving `allowPrivateTarget` out keeps whatever is stored. Changing a rule's
name cannot silently re-arm a guard the operator turned off on purpose, or turn
one off they never touched.

### What lands on the endpoint

One POST, `Content-Type: application/json`, `User-Agent: polyemesis`, and **no
signature**. An alert is for a person to read; the signed, never-coalesced,
one-delivery-per-transition counterpart for a script is a hook
([HOOKS.md](HOOKS.md)).

The `json` format's envelope:

```json
{
  "source": "polyemesis",
  "rule": "On-call",
  "sentAt": "2026-03-04T21:14:52.004311Z",
  "alerts": [
    {
      "type": "destination.down",
      "severity": "critical",
      "key": "destination:3",
      "title": "Destination down: YouTube main",
      "text": "YouTube main has not been delivering for 22s.",
      "count": 1,
      "firstAt": "2026-03-04T21:14:52.004311Z",
      "lastAt": "2026-03-04T21:14:52.004311Z",
      "fields": {
        "destination": "YouTube main",
        "platform": "youtube",
        "error": "Connection reset by peer"
      }
    }
  ]
}
```

**`alerts` is an array, and a receiver written as though it holds one item
silently ignores every coalesced sibling.** Coalescing groups by *subject*:
`key` is the subject — `ingest:1`, `disk`, `destination:3`, `destination:3:speed`,
`loudness:3`, `failover:1`, `clipping:track1:1` — and `count` is how many times it was raised
inside the debounce window, so one delivery routinely carries several unrelated
subjects at once. At most ten items fit; `overflow` is how many subjects did
not, stated rather than quietly lost — it is `omitempty`, so the key is absent
altogether when nothing overflowed, which is why the example above has no
`overflow` at all. Read it as "0 if missing". Ten because Discord refuses more
than ten embeds outright, and the same bound is applied to all three formats so
they cannot disagree about what one message is.

**`sentAt` is not a clock reading.** It is the newest `lastAt` in the batch, not
the wall-clock moment of the POST, which is what makes an encoded payload a pure
function of its delivery and comparable in a test — so in a single-item payload
`sentAt` and that item's `lastAt` are always the identical timestamp, as above.
It cannot be subtracted from anything to measure delivery lag; use your
receiver's own arrival time for that.

**Every stream condition names its programme.** Each programme is watched
separately, so on an install with more than one, `ingest.lost`,
`failover.switched` and `audio.clipping` would otherwise read the same for every
one of them. Their `key` ends in the programme's id (`ingest:1`, `failover:1`,
`clipping:track1:1`), their `title` ends in its name (`Ingest lost: Studio B`),
and every stream condition carries `sourceId` and `sourceName` in `fields`.
`disk.low` and `disk.recovered` are the exception: the recordings volume
belongs to the whole install, so they carry no programme, keep the key `disk`,
and are delivered **once per install** however many programmes are running:
one `disk.low` when the first programme sees the volume fill (and another if a
later one sees the recorder halt, so a `critical` floor still hears it), and
one `disk.recovered` when the last programme that saw it low sees it clear.
Deleting a programme while the disk is low does not use up the alert: the next
fill is reported as usual.

`text` and `fields` are omitted when empty, and `fields` is an object rather
than a list because the consumer of a generic webhook is a script and a script
wants to index by name. Everything in both has been through the redactor on the
way in, so nothing here has ever been near a stream key.

### The stream conditions, and what trips them

Ten of the subscribable types are the stream conditions. The thresholds are
stated because an operator picking events in the rule editor needs to know what
is behind each name, and because a dwell time is not a delay — it is the
difference between an alert and a mute button.

| Event | Severity | Fires when |
|---|---|---|
| `ingest.lost` | `critical` | no data has arrived on a programme's ingest for **20 seconds**. Longer than a supervisor restart cycle on purpose: FFmpeg reconnecting to an RTMP endpoint is normal operation, not an incident. Nothing is raised when no ingest is configured at all — silence is then the expected state |
| `ingest.recovered` | `info` | the source is delivering again |
| `destination.down` | `critical` | an **enabled** destination has not been delivering for **20 seconds**. A destination you turned off is not down, and turning it back on starts the clock fresh rather than firing on the time it spent disabled |
| `destination.recovered` | `info` | it is delivering again |
| `failover.switched` | `warning`, or `info` when the switch is *back to* primary | the source selector changed tier. On every switch, because discovering at the end of a broadcast that you streamed the backup all night is the failure this exists to prevent. The first snapshot after a restart adopts the running count rather than greeting you with every switch since boot |
| `audio.clipping` | `warning` | an ingest channel has been at or above **-0.1 dBFS for 3 consecutive observations**. Digital full scale is 0, and -0.1 is where a limiter is already working; one sample is a snare hit, several in a row is a level problem. The counter resets after firing, so the next alert needs another full run |
| `disk.low` | `warning`, or `critical` once the recorder has already halted | free space on the recordings volume is under **2 GiB** *or* under **5%** — either one trips it, because a small volume runs out of percent slowly and a large one runs out of gigabytes slowly, and both end the same way. No dwell time: disk space does not flap, and a recorder that has already stopped writing should not wait twenty seconds to say so. A volume that cannot be measured says nothing rather than alerting on the zero it reads |
| `disk.recovered` | `info` | there is room again |
| `loudness.out_of_compliance` | `warning` | a destination has been outside its loudness target for **90 seconds**. EBU R128 integrates over the whole programme, so a minute and a half of drift is a mix that is wrong rather than a quiet passage. Carries the measured and target LUFS |
| `loudness.recovered` | `info` | it is back inside the target |

`destination.falling_behind` and `destination.caught_up` are stream conditions
too and have their own section below, because the measurement behind them needs
explaining. `broadcast.fault` follows it, and is the one that is about the
stream's platform rather than the stream. The *Send test message* button raises
`test`, which is never coalesced and never filtered — a test a rule quietly
swallows teaches the operator nothing — and is not subscribable.

### A destination falling behind realtime

`destination.falling_behind` fires when a destination stops keeping up, and
`destination.caught_up` closes it out. It is an **earlier** signal than
`destination.down`: a destination is usually degraded for a while before its
FFmpeg child gives up.

The measurement is how fast that destination's output time advances against
the wall clock, over the last **20 seconds**. What makes it useful here is that
video is passed through untouched, so there is barely any encoding work to be
slow at. **A passthrough destination sitting under 1.0 means FFmpeg is blocking
on the write to the platform** — and one whose sink has stopped reading
entirely reads 0, so a stalled destination is caught while its process is still
`running`.

It is deliberately *not* the `speed=` FFmpeg prints. That figure is averaged
over the whole run, so it barely moves during a stall — and it arrives in the
same progress report that stops arriving when a sink stalls. Judged on it, the
alert fired only after a stall had healed, then stayed raised for most of an
hour while the average recovered, so `caught_up` never came and the next stall
could not alert.

That is close to the question a platform's own health API would answer, and it
is answered for *every* destination — including one configured from a pasted
stream key, and a custom RTMP or SRT URL that no API knows about.

The event reports what was measured and hedges the cause on purpose. A slow
uplink, a platform throttling you, and a slow disk under a file destination all
look identical from here, so it says the speed and the frame counts and leaves
the diagnosis to you. The two frame counters are what tell the two apart:

| Rising | Means |
|---|---|
| dropped frames | FFmpeg is discarding to keep up — the **output** is congested |
| duplicated frames | FFmpeg is padding — the **source** is starving |

Thresholds are a rate under `0.95` sustained for 30 seconds. Both are
deliberately conservative: a dip at a keyframe boundary is normal and an alert
that fires on one is an alert you mute. Nothing is measured until a
destination's process has moved some media, or while it is not running — so
nothing fires while a destination is starting up or after it has stopped
(`destination.down` covers that) — and nothing is measured while the ingest is
lost, because every destination stops then and `ingest.lost` already says so.
Each stall raises its own `falling_behind`, and each recovery its own
`caught_up`.

**Every `falling_behind` is closed by exactly one message.** A stall often ends
with the platform resetting the connection, so the process exits and is
respawned within a couple of seconds — far too briefly for `destination.down`.
The alert stays open across that gap, and `caught_up` closes it once the new
run has been measured at realtime (about ten seconds of output). If the gap
lasts long enough to be a `destination.down`, then `destination.recovered`
closes both, and no `caught_up` follows. A respawned run that is still slow
sends nothing until it catches up. Only disabling or deleting the destination
closes the alert without a message, because you did that yourself.

The same numbers are on each destination's card, live.

### A broadcast that will not start or end

`broadcast.fault` (`warning`) fires when a platform refuses to move a
broadcast's state and somebody has to act — the channel is at its
concurrent-broadcast limit, the broadcast has already been completed and cannot
return to live, the connected account's token expired.

**It is not `destination.down`, and reading it as one sends you to look at the
thing that is working.** The stream is fine: bytes are flowing, FFmpeg is
healthy, the destination is delivering. What has failed is the platform's idea
of the broadcast, so the symptom an operator sees is a watch page that says
"starting soon" beside a stream that is going out perfectly.

**polyemesis never stops a stream because a transition failed.** The platform
requires an active ingest to accept a transition at all, so stopping the stream
would destroy the only condition under which a retry could ever succeed. The
whole response to a failure is therefore to tell you: the fault appears on the
destination card, raises this alert, and sends a `broadcast.fault` webhook. The
stream carries on.

A crashed encoder is deliberately **not** one of these, and does not end the
broadcast either. A completed broadcast cannot return to live, so ending on a
crash would permanently destroy a show that the supervisor is about to
reconnect to the same key and the same bound stream. If nothing ever
reconnects, the platform's own automatic stop closes the broadcast.

The current phase, the retry count and the fault text are on the destination in
the API, under `lifecycle`.

### Security and configuration events

Eleven of the subscribable types are not about the stream. They are about the
server itself, and they answer one question: *was that me?*

| Event | Severity | Fires when |
|---|---|---|
| `auth.login.failed` | `warning` | sign-ins from one address have passed the throttle's free allowance — **not** on the first mistyped password |
| `auth.login.succeeded` | `info` | a sign-in was accepted; carries how many failures preceded it |
| `auth.password.changed` | `critical` | the admin password was replaced |
| `auth.token.created` | `critical` | an API token was minted; names the token **and its scope**, which is the difference between a credential that can read the dashboard and one that can delete a destination |
| `auth.token.revoked` | `warning` | an API token was destroyed; names the same token the created event named |
| `settings.changed` | `warning` | a settings save altered the stored document, **or** the MQTT broker password or automod key was rotated |
| `upgrade.staged` | `critical` | a new release was staged over **this server's own binary**. It takes effect at the next restart; a forced stage says so in a named field |
| `upgrade.rolled_back` | `critical` | the previous binary was restored. Names no version — a rollback restores whatever this box ran before, and inventing a tag would be a guess printed as a fact |
| `debug.exported` | `critical` | a debug bundle was downloaded, with how many log records and whether the capture was truncated |
| `clip.captured` | `info` | a clip was cut from the replay buffer |
| `alerts.rule_changed` | `warning`, or `critical` for a delete | an alert rule was created, edited or deleted; names the rule, never its URL. **A deleted rule is sent this event itself**, on its way out, if it was enabled and would have wanted it |

**The five `critical` ones are what belong on a phone**, and they are one story
rather than five. (Deleting an alert rule is `critical` too; it is covered
below.) Changing the password evicts every existing session; minting a
token creates a credential that survives the password change; replacing the
binary creates something that survives the password change, the token revocation
**and** the restart — and the restart is what arms it. A rollback is a binary
replacement in the other direction, and "somebody quietly returned this box to
the version before the security fix" is exactly the sentence this trail exists to
make findable. `debug.exported` is not a breakage at all: it is the moment a copy
of this server's own logs leaves the operator's control, and since polyemesis
keeps no copy of the bundle — a second place credentials could be read from — the
event is the **only** durable record that it happened.

All eleven are in `AllTypes()`, so they appear in the rule picker and are delivered
to any rule with an empty event list. A rule that subscribes to everything
receives these whether or not it was written with them in mind.

**Credential rotations raise `settings.changed` too.** The MQTT broker password
and the automod key are sealed straight into the store by their own endpoints
and never travel through `PUT /settings`, so the comparison that produces this
event cannot see them. They publish it themselves, naming the section — `mqtt`
or `automod` — and nothing else. Without that, a channel would report a
cosmetic settings tweak and stay silent about a credential rotation, which is
the wrong way round.

**`clip.captured` is the one that will fire often.** On a busy stream it is
somebody doing their job, repeatedly. It is `info` so that a rule wanting only
incidents can raise its `minSeverity` and keep every other event on this page,
rather than unsubscribing from the type and forgetting it exists. It is here
because a clip is the one operation that takes content off the server.

**These name things and never show values.** `settings.changed` says *which
sections* changed — `ingest, listeners` — and never what they changed to. That
is not squeamishness: the redactor works by recognising the syntax of URLs and
`key=value` pairs, so it cannot see a bare SRT passphrase at all, and the only
reliable defence is never putting a stored value in the message. For the same
reason `auth.login.failed` does not repeat the username that was guessed, and
`auth.token.created` gives the token's name but not its prefix. The scope *is* a
named field on that event, and is the exception that proves the rule: it is a
classification rather than a stored secret, and it is the one thing that tells a
reader triaging a mint at 3am whether to go back to sleep.

#### Upgrading from 0.3.x

**A rule with no event checkboxes ticked means "everything", so such a rule
starts receiving these the moment you upgrade.** That is the default the first
rule you create is saved with, so on most installs it is the rule you have.
Nothing is backfilled to narrow it, because a migration that re-ran on every
start would silently re-narrow a list you had deliberately cleared later, and
being quietly re-narrowed is worse than being loud once.

If it is more than you want, the severity floor is the fast fix: raising a rule
to `warning` drops routine sign-ins, and raising it to `critical` leaves only
the password change and the token mint. Otherwise tick the events you do want,
which turns the rule from "everything" into exactly that list.

#### These are notifications, not an audit trail

There is no audit log: no table you can query, and nothing built to outlive a
rule being deleted. The alert path is lossy on purpose — a full queue drops
events rather than slowing the streaming path down — so under sustained delivery
failure a security event can vanish with only the notifier's `dropped` counter to
show for it.

**Six of the eleven do leave a line in the server log**, and it is worth knowing
which before you conclude an incident left no trace at all. Minting a token
(`api token created`, carrying the name, the prefix — which the alert
deliberately withholds — and the scope), revoking one (`api token revoked`),
staging a binary (`upgrade staged`, carrying the version, whether it was forced,
who did it and from which address) and rolling one back (`upgrade rolled back`)
each write an `INFO` line as well as raising the alert. So does every alert-rule
change (`alert rule created`, `alert rule edited`, `alert rule deleted`, with
the rule's name, its redacted URL and the client address).

The fifth is the failed sign-in, and it does not line up with the alert.
`failed login` is written at `WARN` on **every** rejected attempt — the first
mistyped password included, not only the ones past the free allowance that raise
`auth.login.failed` — and it carries `username`, `remote` and `penalty`, so the
guessed username the alert deliberately withholds is in the log anyway. (The
throttled attempts that never reach the password check write `throttled login`
instead, and raise nothing.)

All of that goes to stderr, so into your journal, and into the in-memory ring
the debug bundle exports. It is a consequence of those handlers being chatty
rather than a trail anybody designed: it rotates away with everything else, and
the other five events on this page leave nothing behind.

Deleting an alert rule is the one change built to be seen by the channel it
silences. The deleted rule is sent `alerts.rule_changed` directly, from the copy
read before the delete, so a channel that goes quiet has been told why, and the
deletion is in the log. What an attacker who deletes your only rule still
leaves unrecorded is everything they do *after* it. If you need a record that
survives the incident, the receiving end of the webhook is where to keep it.

---

## Automation

Everything the UI does is a REST call, and a script can make the same calls with
an API token instead of a session.

**A token carries a scope, and a new one is `read` unless you say otherwise.**
The Create-token dialog defaults to it, and so does the API: a `POST
/auth/tokens` that omits the `scope` field mints a read-only token. That is
deliberate — a scope feature whose default is "everything" protects only the
people who already knew about it — but it means a script written against an
older version of this page will get 403s until its token is reminted.

A `read` token is for **monitoring**. It reads the metadata a dashboard needs:

```sh
curl -H "Authorization: Bearer pmk_..." https://stream.example.com/api/v1/status
curl -H "Authorization: Bearer pmk_..." https://stream.example.com/api/v1/stats
curl -H "Authorization: Bearer pmk_..." https://stream.example.com/api/v1/destinations
```

It does not get content, and it does not get credentials. Stream keys,
passphrases, the publish token and the playback token come back blanked or
masked; a handful of GETs that return a credential or spawn real work — the
expert command endpoints, keyframe extraction, per-account platform stats — are
refused outright; and the live playout media of a stream you have not made
public is refused exactly as it is for a stranger. See
[SECURITY.md](../SECURITY.md) for the full list and the reasoning.

Anything that changes something needs `admin`:

```sh
# 403 with a read token; mint an admin one for this.
curl -H "Authorization: Bearer pmk_..." \
     -X POST https://stream.example.com/api/v1/destinations/3/stop
```

An `admin` token acts as the admin with three exceptions, all of which are the
router's rules rather than a handler's good manners:

- **It cannot create or revoke tokens.** If a leaked token could mint more,
  revoking the one you know about would mean nothing — the holder has quietly
  issued three others. Minting stays behind the password, so revocation is
  final.
- **It cannot replace the server's binary, change the password, or upload
  media.** Those are the browser's, for the same reason: a credential built for
  unattended automation should not be able to write arbitrary bytes to the disk
  the database lives on.
- **It cannot export the debug bundle.** `GET /debug` and `PUT /debug` stay
  token-reachable, so a dashboard can read capture state and an automation can
  start one. `POST /debug/export` mints a copy of the server's own logs to send
  to somebody who does not have the box, which is the largest disclosure in the
  product; that step wants a human present.

Full route reference: [API.md](API.md).

---

## See also

- [HOOKS.md](HOOKS.md) — the machine-readable counterpart to the alerts above:
  one signed delivery per transition, never coalesced, for a script rather than
  a person
- [MQTT.md](MQTT.md) — retained telemetry and Home Assistant discovery
- [API.md](API.md#authentication) — tokens, sessions and CSRF
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — organised by what you observe
- [../SECURITY.md](../SECURITY.md) — what a token can and cannot do
