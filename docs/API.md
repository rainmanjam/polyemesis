# HTTP API

Everything the web UI does, it does through this API. There is no private
back channel — if the UI can do it, so can you.

Base path: `/api/v1`. All responses are JSON except `/metrics`, which is
Prometheus exposition, and the download routes, which return files.

## Authentication

Two mechanisms, resolved in this order:

### 1. Bearer token (for scripts and automation)

```sh
curl -H "Authorization: Bearer pmk_..." https://host:8080/api/v1/status
```

Create tokens in the UI under **Settings → API tokens**, or via
`POST /auth/tokens`. They are stored as hashes — the value is shown **once**, at
creation, and cannot be recovered. Revoke individually with
`DELETE /auth/tokens/{id}`.

**A token cannot manage other tokens.** That is deliberate: a leaked token
should not be able to mint replacements for itself or lock you out.

Bearer requests need no CSRF token — nothing attaches an `Authorization` header
on its own, so there is no cross-site request to forge.

### 2. Session cookie (what the browser uses)

`POST /auth/login` sets an `HttpOnly`, `SameSite=Lax` session cookie and a
readable `polyemesis_csrf` cookie. **Every state-changing request must echo that
value in the `X-CSRF-Token` header.**

```sh
curl -c jar -X POST https://host:8080/api/v1/auth/login \
  -d '{"username":"admin","password":"..."}'

curl -b jar -X PUT https://host:8080/api/v1/settings \
  -H "X-CSRF-Token: $(grep polyemesis_csrf jar | awk '{print $7}')" \
  -d @settings.json
```

For anything scripted, use a Bearer token instead. It is simpler and it is what
tokens are for.

## Conventions

- `{id}` is an integer. A non-numeric one is `400`, not `404` — the route
  matched, the argument did not parse.
- A missing row is `404`. An invalid body or an unsatisfiable request is `400`
  with `{"error": "..."}` saying what is wrong, in language meant for the person
  looking at the screen.
- Lists return `[]`, never `null`.
- **No response ever contains a secret.** Stream keys, client secrets, API
  tokens and TLS private keys are never returned. Webhook URLs come back masked;
  handing the masked form back on an update means "unchanged".

## Routes

### Unauthenticated

| Method | Path | Notes |
|---|---|---|
| `GET` | `/setup` | Whether first-run setup is still needed |
| `POST` | `/setup` | Create the admin. Refused once one exists |
| `POST` | `/auth/login` | Throttled per client address |
| `GET` | `/health` | Liveness |
| `GET` | `/tls/ca` | The generated CA, for trusting a self-signed instance |
| `GET` | `/playout/public` | Public player, when playout is published |
| `GET` | `/playout/poster.jpg` | Poster frame for the public player |

### Session and access

| Method | Path |
|---|---|
| `POST` | `/auth/logout` |
| `GET` | `/auth/me` |
| `POST` | `/auth/password` |
| `GET` `POST` | `/auth/tokens` |
| `DELETE` | `/auth/tokens/{id}` |

### State and telemetry

| Method | Path | Notes |
|---|---|---|
| `GET` | `/status` | Everything the dashboard renders, in one object |
| `GET` | `/system` | Host, FFmpeg, build |
| `GET` | `/stats` | System, bitrate, relay counters |
| `GET` | `/levels` | Current audio levels |
| `GET` | `/source` | Probed track layout |
| `PUT` | `/source/annotations` | Label what each incoming track is |
| `GET` | `/version`, `POST` `/version/check` | |
| `GET` | `/processes`, `/processes/{name}/logs` | A child's own FFmpeg output |
| `GET` | `/metrics` | Prometheus exposition |
| `GET` | `/ws` | WebSocket: status, levels, logs, chat |

### Settings

| Method | Path |
|---|---|
| `GET` `PUT` | `/settings` |
| `GET` | `/tls` |

`PUT /settings` takes the whole blob. Read it, change what you want, write it
back — a partial object will clear what it omits.

### Sources

| Method | Path | Notes |
|---|---|---|
| `GET` `POST` | `/sources` | |
| `GET` `PUT` `DELETE` | `/sources/{id}` | Delete cascades to its destinations and renditions |
| `POST` | `/sources/{id}/token` | Rotate. The old token keeps working for five minutes |

Send only stored fields on a `PUT`. Server-computed ones (`publishUrls`,
`publishing`, `tokenEnforced`) are rejected.

### Destinations

| Method | Path |
|---|---|
| `GET` `POST` | `/destinations` |
| `PUT` | `/destinations/order` |
| `GET` `PUT` `DELETE` | `/destinations/{id}` |
| `POST` | `/destinations/{id}/start`, `/stop`, `/restart` |
| `POST` | `/destinations/{id}/refresh-key` |
| `GET` `PUT` `DELETE` | `/destinations/{id}/expert` |
| `POST` | `/destinations/{id}/expert/preview`, `/dry-run` |

List rows arrive wrapped as `{"destination": ..., "routing": ...}` so the UI
gets the compiled routing without a second round trip.

**Expert mode splices arbitrary arguments into an FFmpeg command line.** Treat
access to it as equivalent to shell access. `dry-run` tells you whether the
result would start, without starting it.

### Routing and renditions

| Method | Path |
|---|---|
| `POST` | `/routing/compile` |
| `GET` | `/routing/presets`, `POST` `/routing/presets/{preset}` |
| `GET` `POST` | `/renditions` |
| `GET` | `/renditions/presets` |
| `GET` `PUT` `DELETE` | `/renditions/{id}` |
| `POST` | `/renditions/{id}/restart` |
| `GET` | `/encoders` |

`POST /routing/compile` returns the filter graph a profile would produce,
without saving anything. Useful for understanding what a selection actually
does.

### Failover

| Method | Path | Notes |
|---|---|---|
| `POST` | `/failover/source` | `{"source": "primary\|backup\|slate\|auto"}` |

`auto` clears a manual pin and returns control to the detector. `400` when
failover is off — there is no tier to switch.

### Playout

| Method | Path |
|---|---|
| `GET` | `/playout` |
| `PUT` | `/playout/publish` |
| `POST` | `/playout/token`, `/playout/analytics/reset` |

### Recordings, library, clipper

| Method | Path |
|---|---|
| `GET` | `/recordings`, `/recordings/usage`, `/recordings/stems` |
| `DELETE` | `/recordings/{id}` |
| `GET` | `/recordings/{id}/download`, `/recordings/stems/{name}/download` |
| `GET` | `/library`, `/library/search` |
| `POST` | `/library/sessions`, `/library/sessions/regroup` |
| `GET` `PUT` `DELETE` | `/library/sessions/{id}` |
| `GET` `PUT` | `/library/recordings/{id}` |
| `GET` `DELETE` | `/library/recordings/{id}/transcript` |
| `PUT` | `/library/recordings/{id}/speaker` |
| `POST` | `/library/recordings/{id}/jobs/{kind}` |
| `GET` | `/library/recordings/{id}/media/{file}` |
| `GET` | `/clipper/recordings/{id}`, `/keyframes`, `/transcript` |
| `POST` | `/clipper/recordings/{id}/plan`, `/export` |
| `GET` | `/clipper/jobs/{id}/download` |

Every download route is confined to the data directory. A name that escapes it
is refused, not served.

### Clips (rolling buffer)

| Method | Path |
|---|---|
| `GET` `POST` | `/clips` |
| `PUT` | `/clips/buffer` |
| `DELETE` | `/clips/{name}` |
| `GET` | `/clips/{name}/download` |

On `PUT /clips/buffer`, a `windowSeconds` of `0` or less means **leave the
window unchanged**, so a page that only toggles the switch does not need to know
the current value.

### Jobs

| Method | Path |
|---|---|
| `GET` | `/jobs`, `/jobs/overview` |
| `GET` `PUT` | `/jobs/policy` |
| `POST` | `/jobs/pause`, `/jobs/resume`, `/jobs/purge` |
| `GET` `DELETE` | `/jobs/{id}` |
| `POST` | `/jobs/{id}/cancel`, `/retry`, `/release` |

### Alerts and schedules

| Method | Path |
|---|---|
| `GET` | `/alerts/meta` |
| `GET` `POST` | `/alerts/rules` |
| `GET` `PUT` `DELETE` | `/alerts/rules/{id}` |
| `POST` | `/alerts/rules/{id}/test` |
| `GET` `POST` | `/schedules` |
| `GET` | `/schedules/runs` |
| `GET` `PUT` `DELETE` | `/schedules/{id}` |

### Platforms, metadata, chat

| Method | Path |
|---|---|
| `GET` | `/platforms/presets`, `/capabilities`, `/guides` |
| `GET` | `/platforms/credentials` |
| `PUT` `DELETE` | `/platforms/credentials/{platform}` |
| `GET` | `/platforms/accounts`, `/platforms/accounts/{id}/stats` |
| `DELETE` | `/platforms/accounts/{id}` |
| `GET` | `/oauth/{platform}/start`, `/callback` |
| `GET` | `/metadata` |
| `POST` | `/metadata/push`, `GET` `/metadata/push/{id}` |
| `GET` | `/chat`, `/chat/messages` |
| `POST` | `/chat/send` |
| `DELETE` | `/chat/messages` |
| `GET` | `/loudness`, `PUT` `/loudness` |

## WebSocket

`GET /ws` upgrades and then pushes status, audio levels, process logs, loudness
reports and chat as they happen. It is the same data the polling routes return —
use it when you want changes rather than snapshots.

## A worked example

Add a destination carrying tracks 1 and 3, mixed to stereo:

```sh
TOKEN=pmk_...
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -X POST https://host:8080/api/v1/destinations \
     -d '{
       "name": "Second language",
       "kind": "rtmp",
       "url": "rtmp://live.example.com/app",
       "streamKey": "...",
       "enabled": true,
       "audioBitrate": 160,
       "profile": {
         "mode": "simple",
         "sampleRate": 48000,
         "normalize": "off",
         "tracks": [
           {"track": 0, "enabled": true,  "gain": 1.0},
           {"track": 1, "enabled": false, "gain": 1.0},
           {"track": 2, "enabled": true,  "gain": 1.0}
         ]
       }
     }'
```

Track numbers are **zero-based in the API** and shown one-based in the UI, which
is why "tracks 1 and 3" is `0` and `2` here.
