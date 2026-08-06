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

**The endpoint requires authentication.** It accepts an API token, which is what
a scraper should use — create one under *Settings → API tokens* and point
Prometheus at it:

```yaml
scrape_configs:
  - job_name: polyemesis
    metrics_path: /api/v1/metrics
    static_configs:
      - targets: ['stream.example.com']
    authorization:
      credentials_file: /etc/prometheus/polyemesis.token
```

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
- Values are in base units — bytes, seconds, bits per second.
- Destinations are labelled `id` and `name`.
- `polyemesis_destination_info` carries `kind` and `platform` for joining.

## Queries to start from

```promql
polyemesis_ingest_up == 0                                   # nobody is streaming
polyemesis_destination_up == 0 and polyemesis_destination_enabled == 1
rate(polyemesis_destination_restarts_total[15m]) > 0        # a flapping output
polyemesis_recording_free_bytes < 20e9                      # disk filling up
```

The second is the one worth alerting on first: a destination that is enabled but
not up is a platform you think you are streaming to and are not.

## Built-in alerts

If you do not want to run Prometheus, polyemesis has its own alert rules with
webhook delivery — the same conditions, evaluated in-process, posted to a URL
you supply. Configure them under *Settings → Alerts*, or through
`/api/v1/alerts` ([API.md](API.md#alerts-and-schedules)).

Webhook URLs often carry their credential in the path, so they are masked in
every API response. Handing the masked form back on an update means "unchanged".

### Security and configuration events

Five of the subscribable types are not about the stream. They are about the
server itself, and they answer one question: *was that me?*

| Event | Severity | Fires when |
|---|---|---|
| `auth.login.failed` | `warning` | sign-ins from one address have passed the throttle's free allowance — **not** on the first mistyped password |
| `auth.login.succeeded` | `info` | a sign-in was accepted; carries how many failures preceded it |
| `auth.password.changed` | `critical` | the admin password was replaced |
| `auth.token.created` | `critical` | an API token was minted |
| `auth.token.revoked` | `warning` | an API token was destroyed; names the same token the created event named |
| `settings.changed` | `warning` | a settings save altered the stored document, **or** the MQTT broker password or automod key was rotated |
| `clip.captured` | `info` | a clip was cut from the replay buffer |

The two `critical` ones are the pair worth putting on a phone. Changing the
password evicts every existing session, and minting a token creates a
credential that survives the password change — between them they are how
somebody who has your password keeps your server.

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
`auth.token.created` gives the token's name but not its prefix.

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

Nothing is written down locally. The alert path is lossy on purpose — a full
queue drops events rather than slowing the streaming path down — so under
sustained delivery failure a security event can vanish with only the notifier's
`dropped` counter to show for it. An attacker who deletes your only alert rule
leaves no local record at all. If you need a record that survives the incident,
the receiving end of the webhook is where to keep it.

---

## Automation

Everything the UI does is a REST call, and a script can make the same calls with
an API token instead of a session:

```sh
curl -H "Authorization: Bearer pmk_..." https://stream.example.com/api/v1/status
curl -H "Authorization: Bearer pmk_..." \
     -X POST https://stream.example.com/api/v1/destinations/3/stop
```

A token acts as the admin with one exception: **it cannot create or revoke
tokens.** If a leaked token could mint more, revoking the one you know about
would mean nothing — the holder has quietly issued three others. Minting stays
behind the password, so revocation is final.

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
