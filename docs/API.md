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
| `PUT` | `/settings/mqtt-password` |
| `PUT` | `/settings/automod-key` |
| `GET` | `/tls`, `/fonts` |

`PUT /settings` takes the whole blob. Read it, change what you want, write it
back — a partial object will clear what it omits.

The MQTT password has its own route because it is the one setting `GET
/settings` will not give back. Writing it through the blob would mean reading
the blob first, which would mean handing the password out to anything that can
read settings. **`/settings/automod-key` exists for the same reason** — the
model API key is sealed and never returned; `automod.model.hasApiKey` is all
the settings blob carries. Sending an empty key clears it.

`GET /fonts` lists the fonts available to a text overlay: the two weights of
Inter that ship embedded, plus anything you drop in `<data-directory>/fonts/`,
with the built-in ones marked. The route exists so the UI offers what this
install actually has rather than a hard-coded list a build or a data directory
could contradict.

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

**A create, update or `refresh-key` may return `warnings`**, an array of
sentences meant to be shown to the operator. It is present only when something
was changed or omitted that they did not ask for, and it never accompanies an
error — the write succeeded, and this says what it did:

```json
{
  "destination": { "...": "..." },
  "warnings": [
    "Compliance settings were removed: kick has no compliance surface, so a
     privacy or COPPA declaration stored here would never be sent."
  ]
}
```

The cases that produce one today are a destination carrying settings its
platform cannot send (see [PLATFORMS.md](PLATFORMS.md#compliance-metadata)), and
a destination that asked for backup ingest and was not offered an endpoint.

**Destination fields added in 0.2.0:** `backupUrl` and `backupStreamKey` — the
platform's secondary ingest, stored when the broadcast was created and empty
when it offered none — `backupIngestWanted`, the operator's request for a
redundant feed, plus `facebook.scheduledFor` and `facebook.broadcastId`.

`backupIngestWanted` is top-level and NOT under `facebook`, which is a change
from earlier 0.2.0 pre-releases: it was `facebook.backupIngest`, and anything
scripting this endpoint against that name must be updated. There is no
compatibility alias, deliberately — the endpoint it gates was never
platform-scoped, and a field readable under two names is the ambiguity the move
exists to remove. Stored rows are migrated on first open; only clients that
write the field are affected.

**Status fields added in 0.2.0:** a destination's live status carries
`backupProcess` (the redundant feed's own process state, absent when there is
no backup), `backupError` (why a requested backup does not exist) and
`facebookBroadcastId` (the pre-announced broadcast, which the dashboard links
to). `backupProcess` is deliberately separate from `process`: a backup that has
been dead for an hour beside a healthy primary is the one state this must not
hide.

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

### Media uploads

| Method | Path |
|---|---|
| `POST` | `/media` |
| `GET` | `/media` |
| `DELETE` | `/media/{name}` |

`POST /media` takes `multipart/form-data` with the file in a part named `file`.
It streams to disk rather than buffering, so a multi-gigabyte upload is not an
allocation.

**The filename you send is a hint and is discarded.** The server chooses the
stored name, with a random suffix, because this is the only endpoint where a
caller supplies both the bytes and something path-shaped. The response carries
the name it chose and a `pullUrl` ready to paste into a pull source.

Uploads are stored under `<data-directory>/uploads/`, which retention never
sweeps — a policy written about footage the server captured must not delete a
file an operator deliberately put there. Every file carries an `origin` of
`uploaded`, `recorded` or `clip`, derived from which store it came out of rather
than stored beside it.

`POST /media` and `DELETE /media/{name}` are **session-only**: a browser session
reaches them and **an API token does not**. Writing arbitrary bytes to the
server's disk is not something a leaked automation credential should reach.
`GET /media` is not restricted — a token can list what is stored, which is the
half of this endpoint automation actually wants.

Until this was fixed, the sentence above was the only thing enforcing it: the
routes were in the ordinary authenticated group, and a token-only `POST`
succeeded. They now sit in a session-only router group, which is what makes the
statement checkable rather than aspirational.

Refusals worth knowing: `413` over the size limit, `507` when the volume lacks
room — checked *before* the write, because a filled disk takes the database and
the HLS preview with it — and `400` for an empty file. None of them leaves a
partial file behind.

### Automod

| Method | Path |
|---|---|
| `GET` | `/automod/matrix` |
| `GET` | `/automod/stats` |

Automod's *configuration* lives inside `/settings` — the matrix, the rules and
the model options all round-trip through that blob. Only two things need routes
of their own.

`GET /automod/matrix` renders every cell with an `available` flag and, where it
is false, a `reason`. **Availability is derived from what each platform can
actually do and is never stored**: a switch offering an action a platform cannot
perform fails silently, and the operator believes that channel is protected. The
response also carries the `actions`, `checkers` and `platforms` vocabularies, so
a client builds its table from the server's list rather than a second copy free
to drift.

`GET /automod/stats` reports model spend and health — calls this hour against
the ceiling, failures, and the last error.

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

### Lifecycle webhooks

Signed POSTs on stream and destination transitions. One delivery per
transition, in order, for a script. See [HOOKS.md](HOOKS.md) for the envelope
and how to verify a signature.

| Method | Path |
|---|---|
| `GET` | `/hooks/meta` |
| `GET` `POST` | `/hooks` |
| `GET` `PUT` `DELETE` | `/hooks/{id}` |
| `POST` | `/hooks/{id}/test` |
| `GET` | `/hooks/{id}/deliveries` |

`POST /hooks` is the only call that ever returns the signing key, and it returns
it once. The stored URL is masked everywhere it is read back, so an edit that
submits the masked value unchanged keeps the real one.

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

A schedule create or update may also return `warnings`, on the same terms as a
destination write. The one that exists today: a `once` schedule firing further
ahead than Facebook accepts a scheduled broadcast gets no event page, and is
told so. The schedule still saves and still runs.

### Platforms, metadata, chat

| Method | Path |
|---|---|
| `GET` | `/platforms/presets`, `/capabilities`, `/guides` |
| `GET` | `/platforms/credentials` |
| `PUT` `DELETE` | `/platforms/credentials/{platform}` |
| `GET` | `/platforms/accounts`, `/platforms/accounts/{id}/stats` |
| `DELETE` | `/platforms/accounts/{id}` |
| `GET` | `/oauth/{platform}/start`, `/callback` |
| `GET` | `/metadata`, `/metadata/broadcast-window` |
| `POST` | `/metadata/push`, `GET` `/metadata/push/{id}` |
| `GET` | `/chat`, `/chat/messages`, `/chat/search`, `/chat/users` |
| `POST` | `/chat/send` |
| `DELETE` | `/chat/messages` |
| `POST` | `/chat/messages/hide` |
| `POST` `DELETE` | `/chat/bans` |
| `PATCH` | `/chat/settings` |
| `GET` | `/loudness`, `PUT` `/loudness` |

`/metadata/broadcast-window` reports the period each platform will accept a
scheduled broadcast in, because they disagree and the composer has to say so
before you fill the form in rather than after.

#### Moderation

`/chat/users` is the moderator's user card: what one person has said, newest
last. It reads polyemesis's own retained scrollback, not the platform — **no**
platform here publishes an API for a user's message history, Twitch included.
Its mod card is a web-app feature backed by internal endpoints. The trade is
depth for breadth: shallower than Twitch's card, and it works across all four
platforms at once.

`/chat/search?q=` finds a message again, matching on its text **or its author's
name**, newest first — the one read here that is not chronological, because a
result list answers "where did that comment go" and burying the likeliest answer
at the bottom would be perverse. `platform=` narrows it to one tab and `limit=`
bounds the page.

It searches the database and never the Hub's in-memory ring, which holds only
what the current process has seen; "find the comment from earlier" is precisely
the question a process-lifetime buffer cannot answer. The same caveat as
`/chat/users` applies and applies harder: the response carries `retentionNote`
and `truncated` because search is the one place an operator can conclude
something did *not* happen. **An empty result means "not in the scrollback we
kept", never "never said"** — so render the note alongside no-results, not only
alongside a full page.

`DELETE /chat/messages` removes one message on the platform. `POST
/chat/messages/hide` is Facebook's reversible hide where the platform offers it,
and a local-only hide everywhere else — the pane stops showing it, the platform
never hears about it.

`POST` and `DELETE /chat/bans` ban, time out, and lift either. **The duration is
a Go duration, and the adapters convert.** YouTube and Twitch count seconds;
Kick counts *minutes*, so a unified `600` would mean ten minutes on two
platforms and seven days on the third. Each adapter converts at the last moment
and rounds **up**, because truncating 30s to zero minutes would reach Kick as a
permanent ban.

`PATCH /chat/settings` is Twitch's channel rules — slow mode, followers-only,
subscribers-only, no repeated messages.

One deletion trap is worth stating because the platform's own API hides it:
`DELETE /helix/moderation/chat` with **no** `message_id` deletes every message in
the channel and returns success. polyemesis refuses an empty id before the URL
is built.

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
