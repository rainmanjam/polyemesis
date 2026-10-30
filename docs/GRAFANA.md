# Grafana dashboard and Prometheus alerts

polyemesis ships a Grafana dashboard, a set of Prometheus alert rules, and a
compose file that runs Prometheus and Grafana with both already loaded. This
page sets them up. [MONITORING.md](MONITORING.md) explains every metric the
dashboard draws.

- [What ships](#what-ships)
- [Quick start with Docker Compose](#quick-start-with-docker-compose)
- [Scraping a native install](#scraping-a-native-install)
- [Using your own Prometheus and Grafana](#using-your-own-prometheus-and-grafana)
- [The dashboard](#the-dashboard)
- [The alert rules](#the-alert-rules)
- [Security](#security)

---

## What ships

| File | What it is |
|---|---|
| [`deploy/grafana/polyemesis.json`](../deploy/grafana/polyemesis.json) | The dashboard. Grafana 10 or later. |
| [`deploy/prometheus/alerts.yml`](../deploy/prometheus/alerts.yml) | Prometheus alerting rules. |
| [`deploy/monitoring/`](../deploy/monitoring/) | `docker-compose.yml` for Prometheus and Grafana, the scrape config, and Grafana's provisioning. |

A test in CI reads the dashboard and the rules and fails if either queries a
metric polyemesis does not export. A renamed metric therefore cannot leave a
panel reading "No data" without somebody noticing.

## Quick start with Docker Compose

This runs Prometheus and Grafana beside a polyemesis started from the
repository's `docker-compose.yml`, which publishes the server on port 8080.

1. **Create a token.** In polyemesis, open *Settings → API tokens* and create a
   token with the **read** scope. A read token is enough to scrape.
2. **Save it** on its own, with nothing else in the file, as
   `deploy/monitoring/polyemesis.token`. That path is git-ignored.

   ```sh
   cd deploy/monitoring
   printf '%s' 'pe_...your token...' > polyemesis.token
   chmod 600 polyemesis.token
   ```

3. **Start both**, with a Grafana admin password of your choosing:

   ```sh
   GRAFANA_ADMIN_PASSWORD='a long password' docker compose up -d
   ```

4. **Open Grafana** at <http://127.0.0.1:3000> and sign in as `admin`. The
   polyemesis dashboard is the home page, and is also under *Dashboards →
   polyemesis*.

Prometheus is at <http://127.0.0.1:9090>. *Status → Targets* should show the
`polyemesis` job as **UP**. If it shows `401 Unauthorized`, the token file is
wrong: check that it holds only the token, and that the token has not been
revoked.

Both ports are bound to `127.0.0.1`. From another machine, use an SSH tunnel:
`ssh -L 3000:127.0.0.1:3000 your-server`.

**Plain HTTP here is deliberate, and it is only acceptable on one machine.**
The token travels in the clear on every scrape. The example scrapes
`host.docker.internal:8080`, which never leaves the host. Do not point a plain
`http` scrape across a network you do not control.

## Scraping a native install

An install made with `install.sh` terminates TLS itself, on port 443 by
default, with a certificate from its own local CA. Edit
`deploy/monitoring/prometheus.yml`: replace the `scheme`, `static_configs` and
the comment block under it with the HTTPS form already written there.

```yaml
    scheme: https
    static_configs:
      - targets: ['stream.example.com:443']
    tls_config:
      ca_file: /etc/prometheus/polyemesis-ca.crt
```

Copy the install's `<dataDir>/tls/ca.crt` next to `prometheus.yml` and mount it
in `docker-compose.yml`, under the `prometheus` service's `volumes`:

```yaml
      - ./polyemesis-ca.crt:/etc/prometheus/polyemesis-ca.crt:ro
```

Delete the `tls_config` lines if the install has an ACME or other publicly
trusted certificate. Never use `insecure_skip_verify`: it sends the token to
whoever answers. [MONITORING.md](MONITORING.md#the-metrics-endpoint) explains
why the scheme and port must both be stated.

## Using your own Prometheus and Grafana

**Prometheus.** Add the `polyemesis` scrape job from
`deploy/monitoring/prometheus.yml`, and the rules file:

```yaml
rule_files:
  - /path/to/polyemesis/deploy/prometheus/alerts.yml
```

Check it before you reload: `promtool check rules alerts.yml`.

**Grafana.** Either:

- **Import it.** *Dashboards → New → Import*, then upload
  `deploy/grafana/polyemesis.json`; or
- **Provision it** from disk, with a dashboard provider like
  `deploy/monitoring/grafana/provisioning/dashboards/polyemesis.yml`.

The dashboard has no hard-coded datasource. It asks for one with its
**Prometheus** picker, so it works however your datasource is named. Set the
datasource's *Scrape interval* to your scrape interval (15s in the example), so
that `$__rate_interval` is never shorter than two scrapes.

## The dashboard

The **Programme** picker at the top filters every ingest and destination panel
to the programmes you choose. Destinations are matched through their
programme, not by id. Under **All**, a destination that belongs to no programme
is included too.

| Row | What it shows |
|---|---|
| Overview | Programmes, ingests receiving, destinations delivering, enabled destinations that are **not** delivering while their programme is receiving, held destinations, engines down, uptime and version |
| Ingest | Bitrate arriving per programme, whether anything is arriving and whether the programme has a running engine, ingest restarts, and relay throughput, drops and subscribers |
| Destinations | Whether each destination is delivering; **why each failing destination is failing, and whether it is held**; failures by cause over time and over the whole range; delivery speed; output bitrate; restarts, dropped frames and stalls |
| Host | Host and polyemesis CPU, as a share of the whole host; memory |
| Recording | Free space on the recordings volume, used space, and the segment count |
| Viewers and QoE | A placeholder. Viewer counts and player quality metrics are not exported yet, and this row fills in when they are. |

**"Delivering" means running and moving.** A destination whose platform stops
taking data keeps its FFmpeg process running; it reads *Down* here anyway,
because its output has stopped. A lost ingest is not counted against the
destinations. See [MONITORING.md](MONITORING.md#queries-to-start-from).

**"Receiving" is the bitrate, not `polyemesis_ingest_up`.** `_up` says the
programme is able to receive: its ingest process is running, or, for an SRT
source, the shared SRT listener is bound. Either way it stays 1 while the
encoder is away, so it cannot say whether anything is arriving. The bitrate
can, and the panel draws that, with `polyemesis_source_engine_up` beside it.

**Between shows, destinations read *Up*, not *Down*.** Once the encoder
disconnects, a running destination's output stops because there is nothing to
send. That is the ingest's news, so the destination is not reported as
stalled. `PolyemesisDestinationDown`, and the *Enabled, not delivering* count
on the overview, still count a destination only while its programme is
receiving, so a destination that was stopped on purpose between shows does not
page.

**The failure cause is the one the destination card shows**, from
[Why a destination is failing](MONITORING.md#why-a-destination-is-failing):
`key_rejected`, `platform_not_ready`, `dns`, `connection_refused`, `timeout`,
`tls`, `codec_rejected`, `upload_too_slow` or `unknown`. The dashboard shows the
code, never FFmpeg's line, which may contain the stream key.

## The alert rules

| Alert | Fires when | For |
|---|---|---|
| `PolyemesisDestinationDown` | An enabled destination is not delivering **while its programme's ingest is receiving**. The description names the cause and says whether the destination is held. | 2 min |
| `PolyemesisIngestDown` | A programme has no running engine, or its ingest has failed: its ingest process stopped retrying, or, for an SRT source, the SRT port is configured and the listener is not bound. | 1 min |
| `PolyemesisRecordingDiskLow` | The recordings volume has under 2 GiB or under 5% free, the same floor as the built-in `disk.low` alert. | 5 min |
| `PolyemesisDestinationRestartStorm` | A destination restarted more than 5 times in 15 minutes. | — |
| `PolyemesisIngestRestartStorm` | A programme's ingest restarted more than 5 times in 15 minutes. | — |

Two signals are left out on purpose:

- **"Nobody is streaming"** (ingest bitrate 0). Between shows that is normal,
  so as a page it would fire every night. If you want it, use the guarded query
  in [MONITORING.md](MONITORING.md#queries-to-start-from) with a schedule in
  Alertmanager.
- **The server itself being down.** If the scrape fails, every rule above goes
  quiet. Alert on Prometheus's own `up{job="polyemesis"} == 0`, as you would
  for any target.

The rules only evaluate; they do not deliver. Routing a firing alert to email,
chat or a pager is Alertmanager's job, which the example does not run.
polyemesis's own built-in alerts deliver directly — see
[MONITORING.md](MONITORING.md#built-in-alerts). The two are independent; use
either or both.

## Security

- **The scrape needs a token.** `/api/v1/metrics` refuses a request without
  one. A **read** token is enough. Revoke it under *Settings → API tokens* and
  the next scrape fails with 401, without restarting anything.
- **No secret reaches a label.** Labels carry ids, the names you typed for
  programmes and destinations, kinds, platforms, process states, and the cause
  code. Stream keys, URLs and FFmpeg's output are never labels. A test pins
  this.
- **Keep Grafana and Prometheus private.** Prometheus has no authentication of
  its own, and anyone who can reach it can read every series. The example binds
  both to `127.0.0.1`.

## Publishing the dashboard on grafana.com

The same `polyemesis.json` can be uploaded to grafana.com as it is. It needs no
`__inputs` section, because its datasource is chosen with a template variable.
