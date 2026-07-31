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

- [MQTT.md](MQTT.md) — retained telemetry and Home Assistant discovery
- [API.md](API.md#authentication) — tokens, sessions and CSRF
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — organised by what you observe
- [../SECURITY.md](../SECURITY.md) — what a token can and cannot do
