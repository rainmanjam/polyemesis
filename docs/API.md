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

**A token cannot manage other tokens**, change the account password, upload or
delete media, or complete an OAuth connect flow. Those are session-only: a
leaked token should not be able to mint replacements for itself, lock you out,
write arbitrary bytes to the server's disk, or attach a platform account.

Bearer requests need no CSRF token — nothing attaches an `Authorization` header
on its own, so there is no cross-site request to forge.

#### Token scopes

Every token carries a scope, chosen when it is created:

| Scope | Reaches |
|---|---|
| `read` (default) | **Metadata, not content.** Every `GET` except the thirteen denied below, plus `POST /version/check` and `POST /routing/compile` — the two POSTs that compute an answer and write nothing. Everything else is `403`. |
| `admin` | Everything a signed-in operator can do, minus the session-only routes above. |

The middleware also lets `HEAD` through, and no route in this API is registered
for it, so `HEAD` on any of them is `405` whatever scope you hold. Use `GET`.

```sh
curl -X POST -H "Content-Type: application/json" \
  -d '{"name":"prometheus","scope":"read"}' \
  https://host:8080/api/v1/auth/tokens
```

**Omitting `scope` mints a `read` token.** A client that has never heard of
scopes gets the credential that cannot change anything, which is the only
default that protects anyone who has not already read this page.

The rule is shaped by HTTP method rather than by a list of routes, and that is
deliberate: a route added to this API tomorrow is refused to `read` tokens by
construction, with no table anyone has to remember to update. The small
allowlist above is additive, so forgetting to extend it denies a request that
should have been allowed — never the reverse. `POST
/destinations/{id}/expert/dry-run` is deliberately *not* on it, despite writing
nothing to the database: it spawns FFmpeg with a caller-supplied argument list.

#### What the method rule cannot see

A rule about the HTTP verb cannot tell that one `GET`'s **response body is
itself a credential**, and it cannot tell that another `GET` **does real work**.
Both are handled explicitly, because both were real:

**Credentials are blanked or masked in the response.** For a `read` token — and
only for a `read` token — these come back empty or with the secret part
replaced by `[redacted]`, while the surrounding field is left readable:

| Route | Withheld from a `read` token |
|---|---|
| `GET /sources`, `GET /sources/{id}` | `token`, `publishUrls`, `legacyRtmpKey`, `ingest.srt.passphrase`, `ingest.rtmp.streamKey`, `ingest.pull.url` |
| `GET /settings` | the same `ingest.*` fields, `failover.backup.{srt.passphrase,rtmp.streamKey,pull.url}`, `mqtt.brokerUrl` |
| `GET /system` | the credential parts of `ingestUrl` — the SRT `passphrase` parameter, and a pull URL's `user:pass@` |
| `GET /settings` | also `automod.model.endpoint` — the sealed key table protects the key you typed there, not one pasted into the URL as `?api_key=` |
| `GET /destinations`, `GET /destinations/{id}` | `streamKey`, `backupStreamKey`, `extraInputArgs`, `extraOutputArgs`, and the userinfo in `url` / `backupUrl` (an Icecast mount's password) |
| `GET /playout` | `token` and all three `urls`, each of which embeds it |

`extraInputArgs` and `extraOutputArgs` are there because
`GET /destinations/{id}/expert` is refused to a `read` token for returning the
resolved FFmpeg argv with the stream key in it — and those two fields *are* that
argv, as you typed it. The same bytes cannot have two answers depending on which
route serves them.

A `kind: file` destination's `url` is a **filename**, not a URL, and it comes
back intact. Redacting it would delete a field that never held a credential.

**Values are blanked or masked, not removed** — so a client that reads, edits
and PUTs the document straight back still works, and the JSON path of every
redacted field is the same for a `read` token as for an admin. Note the
consequence for the fields tagged `omitempty`: `backupStreamKey`,
`legacyRtmpKey`, `extraInputArgs` and `extraOutputArgs` come back as the literal
string `[redacted]` rather than as `""`, because an empty string would make the
key vanish and change the shape of the document. A field that was genuinely
empty stays absent for everyone.

The one place the shape does differ is `publishUrls` on `GET /sources`, which is
`null` for a `read` token. Each entry is a publish URL in which the token *is*
the address, so there is no masked form of it that is still a URL.

These responses carry `Vary: Authorization, Cookie` and
`Cache-Control: private, no-store`, because their body depends on who asked —
and a principal arrives in either header: a bearer in `Authorization`, the
signed-in operator in `Cookie`.

**Thirteen routes are refused outright**, for three different reasons. Masking
would have been wrong for the first two (expert mode's contract is that the
command shown is the command that runs) and pointless for the next three, which
are `403` because of what they *do*. The last eight are `403` because of what
`read` was decided to mean:

| Route | Why |
|---|---|
| `GET /destinations/{id}/expert` | returns the resolved FFmpeg argv, stream key and all |
| `POST /destinations/{id}/expert/preview` | the same argv |
| `GET /clipper/recordings/{id}/keyframes` | spawns `ffprobe`, once per timeline part |
| `GET /platforms/accounts/{id}/stats` | calls the platform; can refresh **and persist** an OAuth token |
| `GET /metadata/broadcast-window` | the same, once per connected account |
| `GET /recordings/{id}/download` | the recording itself |
| `GET /recordings/stems/{name}/download` | a separated audio stem |
| `GET /clips/{name}/download` | an exported clip |
| `GET /clipper/jobs/{id}/download` | the clipper's output |
| `GET /library/recordings/{id}/media/{file}` | a media file inside a library recording |
| `GET /clipper/recordings/{id}/transcript` | the verbatim transcript |
| `GET /library/recordings/{id}/transcript` | the same, by the library's route |
| `GET /library/search` | hits carry the segment `text`, its `context` and the `speaker` |

The last of those is the one worth reading twice. `GET /library/search` looks
like a metadata query and is not: iterating common words would rebuild whole
transcripts without ever requesting a route with `transcript` in its path. The
list is drawn from what the bytes are, not from what the URL says.

Listing still works. A `read` token sees recordings, clips, stems and sessions,
their durations, sizes and status, and whether a transcript exists — and
`GET /library` still returns the bare list of speaker labels, which is who
appears rather than what was said.

`GET /encoders` stays available, but `?redetect=` needs `admin`: it runs a test
encode per candidate encoder and rewrites the install's capability cache.

`/hls/*`, the dashboard's preview playlist, is now **session-only** — no bearer
of either scope. Requesting a playlist starts the on-demand preview encoder and
polling keeps it running, and hls.js in the console authenticates with the
session cookie anyway.

**Tokens created before scopes existed are `admin`.** They could already do
everything, so the upgrade grandfathers them rather than silently narrowing a
credential some running script is holding — the failure would otherwise land as
a `403` inside unattended automation. Revoke and re-mint to narrow one.

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

Both of these, and the media origin at `/playout/*`, sit outside every
authenticated group — a viewer has no account and never will — and are guarded
per request instead, because "is this stream published" is a setting an operator
flips at runtime while a route table is built once at startup.

**A bearer token gets no privilege here.** The request is judged on the viewer's
terms: an unpublished stream is `404` for everyone except the signed-in console
and an `admin` token, and a published-but-protected stream wants the playback
token in `?t=`, the `X-Playout-Token` header, the `polyemesis_playout` cookie or
an HTTP basic password. A `read` token is treated exactly as an anonymous
caller — the same status, the same body, the same headers. That is `read`
meaning metadata and not content: live media is content, and `Public: false` is
a decision about the resource that a role-level scope must not override.

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
| `GET` | `/upgrade/plan` | What an in-place upgrade would do on this install, and whether it can |
| `POST` | `/upgrade/stage`, `/upgrade/rollback` | Session-only. Staging writes the new binary; rollback restores the saved one |
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
| `GET` | `/failover/playlist` | The slate playlist's current item and its position |

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
| `POST` | `/platforms/credentials/{platform}/check` |
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

`POST /platforms/credentials/{platform}/check` asks the platform whether the
stored client credentials for it are still good, and answers with a verdict —
never with the credential. It is a `POST` because it makes an outbound call, and
it is refused to `read` tokens for the same reason: a route that exercises a
stored secret is not a read, whatever its verb.

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
