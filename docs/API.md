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
delete media, complete an OAuth connect flow, or export a debug bundle. Those
are session-only: a leaked token should not be able to mint replacements for
itself, lock you out, write arbitrary bytes to the server's disk, attach a
platform account, or take a copy of the server's own logs.

The debug split is deliberate and worth stating: `GET /debug` and `PUT /debug`
stay token-reachable, so a dashboard can read capture state and an automation
can start or stop a recording. Only `POST /debug/export` — the step that mints
a file intended to leave the machine — needs a signed-in operator.

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

**Fifteen routes are refused outright**, for three different reasons. Masking
would have been wrong for the first two (expert mode's contract is that the
command shown is the command that runs) and pointless for the next five, which
are `403` because of what they *do*. The last eight are `403` because of what
`read` was decided to mean:

| Route | Why |
|---|---|
| `GET /destinations/{id}/expert` | returns the resolved FFmpeg argv, stream key and all |
| `POST /destinations/{id}/expert/preview` | the same argv |
| `GET /clipper/recordings/{id}/keyframes` | spawns `ffprobe`, once per timeline part |
| `GET /platforms/accounts/{id}/stats` | calls the platform; can refresh **and persist** an OAuth token |
| `GET /destinations/{id}/facebook/stream-health` | the same, for what Facebook sees arriving at its ingest |
| `POST /destinations/{id}/facebook/end-broadcast` | the same, to end the broadcast. A `POST`, so the method rule already denies it — it is listed because this table is read against `readScopeDeniedPatterns`, and a row missing from one side tells the reader nothing about which side is right |
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

`GET /previews` sits in the same session-only group, and is mounted at the root
rather than under `/api/v1` because the preview it describes is. It answers
with one object per running source — the whole grid in one read:

```json
[{"id": 1, "name": "Main", "width": 1920, "height": 1080,
  "outputLive": true, "ingestLive": true, "onAir": "primary"},
 {"id": 2, "name": "Studio B", "width": 1080, "height": 1920,
  "outputLive": true, "ingestLive": false, "onAir": "slate"}]
```

`outputLive` is whether anything is reaching that programme's destinations —
the encoder, a backup, the slate or a playlist. `ingestLive` is whether the
operator's own encoder is arriving, and it only labels the tile: the second row
above is a programme that is broadcasting its slate because the input went
away, and reporting that as a dead tile would hide the thing actually going
out. `onAir` names the tier the picture comes from — `primary`, `backup`,
`slate`, `playlist`, or absent when the selector is not running. `width` and
`height` are the last measured ingest geometry and are absent until a probe
lands.

It exists because the WebSocket's status is **not** source-scoped: every engine
publishes onto the same feed, so a grid built on it redraws each tile from
whichever engine spoke last. This is the only route that answers "what is on
air on each programme" without one `GET /status?source=<id>` per source, and it
is much smaller than N of those — a tile needs a name, whether anything is
arriving, and what is on air.

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
- **`code` is the branch key on a refusal.** It exists so a client can tell one
  refusal from another without matching on the sentence — the sentences are
  written for a person and are translated into fifteen languages, so a
  comparison against one is a comparison that will eventually be false
  everywhere. An error that has nothing to branch on omits the field, and `code`
  absent means exactly that; it is not a category. Three exist today:

  | `code` | Status | Means |
  |---|---|---|
  | `no_source` | `503` | This install has no source yet, so there is no programme to act on. An **empty state**, not a fault: nothing is broken and only the operator can create one. |
  | `source_required` | `400` | There are several programmes and the request did not say which. The request was well formed; the choice was simply not made. Add `?source=<id>` — see the next bullet. |
  | `account_in_use` | `409` | Disconnecting this platform account would cut destinations loose. See below. |

  Reads answer normally on an install with no source — an empty status, an empty
  process list and no levels, which is the truth.

- **`?source=<id>` is how a request names the programme it acts on.** Fifteen
  routes are scoped to one source, and on an install with more than one they
  refuse until the choice is made:

  `GET /status`, `GET /source`, `PUT /source/annotations`, `GET /levels`,
  `POST /failover/source`, `POST /routing/compile`,
  `POST /routing/presets/{preset}`, `GET /processes`,
  `GET /processes/{name}/logs`, `GET /clips`, `POST /clips`,
  `PUT /clips/buffer`, `GET /loudness`, `PUT /loudness` and `GET /ws`.

  **It is a query parameter on every one of them, whatever the method.** Eight
  are GETs with no body to put it in, and one of the POSTs takes a
  full-replacement routing document with no room for a field that is not a
  routing option — so one spelling serves every method rather than two. It is *not* the `source` field
  in the body of `POST /failover/source`: that one names a failover tier
  (`primary`, `backup`, `slate`, `auto`), the query parameter names the
  programme the switch happens on, and a request can carry both.

  ```sh
  curl -H "Authorization: Bearer $TOKEN" \
    "https://host:8080/api/v1/levels?source=2"
  ```

  Omitting it is correct on an install with one source, or none — the single
  programme is unambiguous, which is why every client written before this
  parameter existed keeps working. With two or more it is `400`:

  ```json
  {"error": "this install runs several programmes, so this request must say which one it is for: add \"?source=<id>\". Available: 1 (Main), 2 (Studio B).",
   "code": "source_required"}
  ```

  The refusal lists the ids and names, so a client that has never called
  `GET /sources` can still tell the operator what to pick. A value that is not
  a positive integer is the same `400` `source_required`, reading `"source"
  must be a source id.` followed by the same list. An id that parses but names
  a programme that is not running is `409` — `source 4 is not running, so there
  is nothing here to answer for it` — rather than a quiet fall back to the
  first programme, which is the whole reason the parameter exists.

- **`DELETE /platforms/accounts/{id}` refuses an unconfirmed disconnect while
  destinations are still on the account**, and answers:

  ```json
  {
    "error": "2 destinations are still on this connected account: Main YouTube, Backup. …",
    "code": "account_in_use",
    "destinations": [
      {"id": 3, "name": "Main YouTube", "platform": "youtube", "enabled": true,
       "broadcastId": "AbC123", "phase": "live", "broadcasting": true}
    ]
  }
  ```

  **This is the ordinary answer, not an edge case.** A destination blocks if it
  is `enabled` **or** mid-broadcast, and a normal install leaves its
  destinations enabled — so most disconnects get this back the first time. Send
  the same request again with a body of `{"confirm": true}` to proceed; nothing
  has been deleted when the `409` is returned.

  A confirmed disconnect answers `200` with `{"status": "disconnected"}`, the
  same `destinations` array, and a past-tense `warnings` entry. It is not
  reversible by reconnecting: the destinations' `account_id` is set to `NULL`,
  the reconnected account gets a new row id, and a destination that was
  mid-broadcast is left live with nothing able to end it — the remedy after that
  is the platform's own studio.

  An absent body means unconfirmed. A body that is present and malformed — or
  that misspells the field — is a `400`, so a client that meant to confirm is
  never read as one that did not.
- **A schedule whose `destinationIds` cannot be resolved is refused, not
  emptied.** An empty `destinationIds` means *every destination on this
  install*, which is the correct reading of "no filter given" — but a list that
  arrives with entries and resolves to nothing is a different thing, and it used
  to normalise to empty before validation saw it. A `stop` schedule that named
  three destinations then fired against every broadcast on the box, and stopping
  a YouTube broadcast completes it permanently.

  So both the create and the update route compare the length before and after
  normalisation, and answer `400` when a non-empty list empties. `[null]` and
  `[0]` are the shapes that reach this: JSON `null` decodes to the zero id
  without error, and ids at or below zero are dropped.

  The refusal is qualified for playlist schedules, where an empty list means the
  playlist rather than everything — a `400` describing a consequence that could
  not have happened is its own defect.
- **An archive `quality` outside the supported range is refused.** The value
  reaches the encoder's rate control directly, and nothing downstream can see
  that a picture is bad: a high CRF encodes cleanly, copies every audio track
  bit-for-bit, decodes without error and shrinks enormously, so verification —
  which compares containers, streams and durations — passes it. With
  `replaceOriginal` set, the smaller file is then renamed over a bit-exact
  master. It is the only route in this API that destroys data it cannot
  reconstruct.

  **There are two bounds, and the same value can pass one and fail the other.**
  An archive written *alongside* the original is allowed some headroom, because
  the master survives whatever comes out. An archive with `replaceOriginal` set
  is held to the tighter one — destroying the master and adding a file are not
  the same act, and the API does not treat them as one. Both bounds depend on
  the codec and the chosen encoder rather than being a single number, so the
  refusal names the bound it applied.

  **On upgrade, a job already queued past the bound fails on its next attempt**
  rather than running — the error names the bound and the reason, because a
  queued job that starts failing after an upgrade with an opaque message is the
  worst version of this.
- **No response returns a secret it did not just create.** Stream keys, client
  secrets, API tokens and TLS private keys are never returned on a read, and
  webhook URLs come back masked — handing the masked form back on an update
  means "unchanged". The exception is the moment of creation: minting an API
  token or rotating a hook signing key returns the value once, because there is
  no other moment you could receive it. A source's publish token is readable on
  request by design — an operator has to paste it into an encoder and will come
  back to read it again.

## Routes

### Unauthenticated

| Method | Path | Notes |
|---|---|---|
| `GET` | `/setup` | Whether first-run setup is still needed |
| `POST` | `/setup` | Create the admin. Refused once one exists. Throttled per client address |
| `POST` | `/auth/login` | Throttled per client address |
| `GET` | `/health` | Three named checks. Not every failure is a `503` — see below |
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

#### What `/health` actually reports

A healthy server answers `200` with exactly `{"status":"ok"}` and nothing
else. That body is byte-identical to what this route has always returned,
because existing monitors compare against that string and a richer happy path
would break them to say something none of them read.

Anything else answers with a `status` of `degraded` or `unhealthy` and a
`checks` array naming what failed:

```json
{"status": "degraded", "checks": [
  {"name": "database", "ok": true},
  {"name": "engine", "ok": true, "detail": "1 of 1 source(s) running"},
  {"name": "recordingDisk", "ok": false,
   "detail": "recording is halted by the free-space floor"}
]}
```

The three checks are always all three, in that order. `database` is a real
query, not a nil check — the failures it catches are a file that has gone away
and a volume unmounted under a running process. `engine` fails when sources are
configured and not one engine is running, which is "nothing is being
published"; no sources at all is a fresh install and passes. `recordingDisk`
fails when the free-space floor has halted recording.

**Only `database` and `engine` are fatal, and only those two make the status
`503`.** A `recordingDisk` failure answers `200` with `"status": "degraded"`,
deliberately: a box that has stopped writing recordings is still broadcasting,
and taking it out of a load balancer over it would end the stream to fix the
files. The consequence for whoever wires up the monitoring is that **a full
recording volume is invisible to a check keyed on the HTTP status** — key on
the `status` field instead, and alert on `degraded` as well as on `unhealthy`.

#### The two throttles

`POST /setup` and `POST /auth/login` are rate-limited per client address, and
both answer `429` with a `Retry-After` header carrying whole seconds. The
policy is the same for each: five free attempts, then a delay starting at two
seconds and doubling with every further one to a five-minute ceiling, and the
counter for an address is forgotten after an hour of quiet. The bodies are
`{"error": "too many setup attempts, try again later"}` and `{"error": "too
many failed attempts, try again later"}`.

What they count differs, and the difference is the one that catches
provisioning scripts. `/auth/login` counts **failures**; a correct password
clears the counter immediately. `/setup` counts **every attempt**, incremented
before the body is read and cleared only when an admin is actually created — so
a `POST /setup` refused because an admin already exists still counts. A script
that re-runs its setup step idempotently meets a `429` on the seventh run and
waits two seconds, then four, and so on. Honour `Retry-After`, or call
`GET /setup` first and skip the POST when setup is no longer needed.

### Session and access

| Method | Path |
|---|---|
| `POST` | `/auth/logout` |
| `GET` | `/auth/me` |
| `POST` | `/auth/password` |
| `GET` `POST` | `/auth/tokens` |
| `DELETE` | `/auth/tokens/{id}` |
| `GET` | `/tour` |
| `POST` | `/tour/complete` |

`/tour` is the onboarding tour's "has this operator been offered it already",
kept on the server rather than in the browser so a second machine does not
re-offer it. `GET` returns `{"completed": bool, "completedAt": <unix, 0 if
never>}`; `POST /tour/complete` is idempotent — the first completion wins, and
dismissing the offer writes the same thing as finishing the tour. The `POST`
needs an `admin` token: it is a write to user state, and a read-only credential
does not get to change what another operator's console shows them.

### State and telemetry

| Method | Path | Notes |
|---|---|---|
| `GET` | `/status` | Everything the dashboard renders, in one object |
| `GET` | `/system` | Host, FFmpeg, build |
| `GET` | `/debug` | Debug-mode state: recording on/off, level, and how much is held |
| `PUT` | `/debug` | Start or stop recording, and optionally clear the buffer |
| `POST` | `/debug/export` | Session-only, and **audited**. Downloads the debug bundle; a token of either scope is refused |
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

`/status`, `/source`, `/source/annotations`, `/levels`, `/processes`,
`/processes/{name}/logs` and `/ws` are programme-scoped: on an install with
more than one source each of them needs `?source=<id>` and answers `400`
`source_required` without it. See [Conventions](#conventions). `/stats` is not
— it describes the box, not a programme, and one install running three sources
used to have three samplers of the host disagreeing by a tick.

### Settings

| Method | Path |
|---|---|
| `GET` `PUT` | `/settings` |
| `PUT` | `/settings/mqtt-password` |
| `PUT` | `/settings/automod-key` |
| `GET` | `/tls`, `/fonts` |
| `GET` | `/tls/acme-preflight` |

`PUT /settings` takes the whole blob. Read it, change what you want, write it
back — a partial object will clear what it omits.

**`ingest` is the default source's ingest, not a copy of it.** Once a source
exists, `GET /settings` serves that source's `ingest` block and `PUT /settings`
merges over it and writes it back to the source, so a document read and written
back unchanged changes nothing — including after the Sources page has changed
the mode. With no source, the blob's own block is served, and a change to it is
refused because there is nothing for it to configure.

`GET /tls/acme-preflight?hostname=…` reports what Let's Encrypt would need from
this host — a name it can issue for, a DNS record, port 80, a contact address —
and, in `acme` mode, what it said the last time it refused. Each check is
`pass`, `fail` or `unknown`; `unknown` is the honest answer where this process
cannot see far enough, and only `fail` clears `ready`. **It changes nothing.**
`config.yaml` is not writable by this service and this route does not pretend
otherwise: it tells you what to write. Omitting `hostname` checks
`tls.hostname`. See [TLS.md](TLS.md#switching-to-lets-encrypt).

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

### Services

The platform registry: ingest servers, encoder ceilings and codecs for the
platforms polyemesis knows. Static — the same answer for every install — so
that an operator picks `Twitch` rather than typing an ingest URL.

Seeded from OBS Studio's `rtmp-services` data, and the response carries a
`provenance` string saying so; the ceilings are the platforms' published
figures, not ours.

Platforms that issue a per-channel ingest host (Kick) have an empty `servers`
list and a `note` explaining what to paste instead.

| Method | Path |
|---|---|
| `GET` | `/services` |

### Destinations

A destination that will probably not work is created anyway, with `warnings[]`
describing why — most commonly an RTMP URL with no application path, which the
far end refuses silently. Refusal stays with `Validate`; `warnings` is advice.

| Method | Path |
|---|---|
| `GET` `POST` | `/destinations` |
| `PUT` | `/destinations/order` |
| `POST` | `/destinations/start-all`, `/stop-all` |
| `GET` `PUT` `DELETE` | `/destinations/{id}` |
| `POST` | `/destinations/{id}/start`, `/stop`, `/restart` |
| `POST` | `/destinations/{id}/refresh-key` |
| `GET` `PUT` `DELETE` | `/destinations/{id}/expert` |
| `POST` | `/destinations/{id}/expert/preview`, `/dry-run` |
| `POST` | `/destinations/{id}/facebook/end-broadcast` |
| `GET` | `/destinations/{id}/facebook/stream-health` |

List rows arrive wrapped as `{"destination": ..., "routing": ...}` so the UI
gets the compiled routing without a second round trip.

`start-all` and `stop-all` act on **every** destination — there is no id list
and no selection. Each row is driven through the same code as
`/destinations/{id}/start` and `/stop`, so the bulk control is exactly N presses
of the per-destination button and can never be more destructive than it.

The answer is a list, never a boolean:

```json
{"action": "start", "results": [
  {"id": 3, "name": "YouTube main", "platform": "youtube",
   "outcome": "started", "state": "running"},
  {"id": 4, "name": "Backup RTMP", "platform": "custom",
   "outcome": "failed", "message": "connection refused"}
]}
```

`outcome` is one of `started`, `stopped`, `warned` (it happened and something
about it was not observed — today only the unreaped stop), `failed` (with
`message` saying why) or `skipped` (the caller went away before this row was
reached, so it was not touched). The status is `200` whenever every row was
reached, including when some of them failed: two refusals out of eight is not
"the request failed".

Starts are **paced** — a gap between one destination and the next, so a burst of
FFmpeg children and a burst of near-simultaneous connections to the same platform
do not arrive as one clap. It is a pacing choice about this box, not a limit
derived from any platform's published ceiling. Stops are not paced: tearing down
is local. A paced start of a long destination list is therefore a long request,
and the response is the finished record of what happened.

**`stop-all` ends every YouTube broadcast on the install, permanently.** Stop and
disable are one thing here — `/stop` clears `destinations.enabled`, and the
broadcast lifecycle coordinator ends the broadcast of any destination that is
disabled. A completed YouTube broadcast cannot return to live. Starting again
puts the video back on the wire but does not bring the broadcasts back; a new one
has to be created or announced. This is true of the per-destination `/stop` too —
the bulk route just does it to every row at once.

The two `facebook/` routes act on the live video recorded against THAT
destination rather than on the account, because one account can hold several
broadcasts at once and "end the broadcast" would otherwise be ambiguous in the
exact situation an operator reaches for it.

`end-broadcast` turns the live video into a VOD; the artefact survives, the
broadcast does not come back, and `ended: false` with no error is an ordinary
outcome meaning Facebook accepted the end and has not yet reported it took.
`stream-health` answers `200 {"supported": false, "reason": ...}` when the
destination has never gone live — Facebook is the only platform here that
publishes bitrate and frame rate at all, so its absence elsewhere is a fact
about the platform rather than a gap.

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
| `GET` | `/renditions/{id}/concerns` |
| `GET` | `/encoders` |

`GET /renditions/{id}/concerns?platform=<id>` compares a rendition against that
platform's published figures from `internal/db/platforms.go` and returns one
entry per concern, each carrying the `detail`, the documentation `source` and the
date it was `checked`. An unknown or empty platform is an empty list, not an
error: a custom RTMP destination has nothing published to be outside of, and the
console asks the same question either way.

The comparison lives here rather than in the browser on purpose. Its whole value
is that it reads researched, dated figures out of one committed file, and a
second copy in TypeScript would drift from that file exactly as hand-copied
numbers on the marketing site once did — which is why a guard now asserts those
against `platforms.go` too. The console asks; it does not derive.

`POST /routing/compile` returns the filter graph a profile would produce,
without saving anything. Useful for understanding what a selection actually
does.

It and `POST /routing/presets/{preset}` are programme-scoped and need
`?source=<id>` on a multi-source install — see [Conventions](#conventions).
The routing editor is where the scoping was written: with programme 2 open, a
debounced track-label edit rewrote programme 1's ingest and restarted its live
destinations, and the compile preview beside it was reading the same default
engine — so the page showed a plausible answer for the wrong saved state.

### Failover

| Method | Path | Notes |
|---|---|---|
| `POST` | `/failover/source` | `{"source": "primary\|backup\|slate\|auto"}` |
| `GET` | `/failover/playlist` | The slate playlist's current item and its position |

`auto` clears a manual pin and returns control to the detector. `400` when
failover is off — there is no tier to switch.

**The body's `source` and the query string's `?source=` are different things**,
and a multi-source install sends both: `?source=<id>` names the programme, the
body names the tier within it. Putting Studio B on its slate is
`POST /failover/source?source=2` with `{"source": "slate"}`. See
[Conventions](#conventions).

### Playout

| Method | Path |
|---|---|
| `GET` | `/playout` |
| `PUT` | `/playout/publish` |
| `POST` | `/playout/token`, `/playout/analytics/reset` |

### Media uploads

| Method | Path |
|---|---|
| `POST` | `/media`, `/media/{name}/verify` |
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

`POST /media/{name}/verify` queues a **re-inspection** of a file already on
disk. It answers `201` with the queued job, or `200` when an identical re-check
was already queued or running, `404` when no such upload exists and `503` when
this build has no job queue. The inspection itself happens in the queue, under
the resource policy, because it is an FFprobe against a file that may be several
gigabytes on a box that is also encoding a broadcast — nothing waits on the
answer, so nothing holds a request open for it.

It exists because `verified: false` used to be a dead end. An upload the server
never managed to inspect — the probe runs while the request is open, so a
dropped connection cuts it short — could only be re-inspected by sending the
bytes a second time, which is no remedy at all for a file the operator no longer
has a local copy of.

**It records only what it establishes.** An inspection that concludes writes
`verified` or `refused`, replacing whatever was recorded before, so a file that
passes on the second look stops being refused. An inspection that *cannot run* —
no FFprobe, a file that has since been deleted, a probe cut short — writes
**nothing at all**, and the job fails saying so. `outcome` never moves to
`unverified` because of this endpoint, and a file with no record keeps having no
record: "nobody has read this" and "this server could not read it just now" are
different claims, and every install has uploads predating verdicts entirely.

`POST /media`, `POST /media/{name}/verify` and `DELETE /media/{name}` are
**session-only**: a browser session reaches them and **an API token does not**.
Writing arbitrary bytes to the server's disk is not something a leaked
automation credential should reach, and neither is rewriting the server's
conclusions about bytes already there — `PUT /settings` refuses a playlist item
or pull source naming an upload that is anything but verified or unrecorded, so
the verdict is a gate and not a label. `GET /media` is not restricted — a token
can list what is stored, which is the half of this endpoint automation actually
wants.

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

A `PUT /settings` that arms a cell whose checker is not configured (no enabled
rule, or the model off or without an endpoint) is a 400 that names the cell.
`summary` counts only the cells that can fire. See
[AUTOMOD.md](AUTOMOD.md#through-the-api).

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

**`DELETE /library/recordings/{id}/transcript` takes an optional `?track=N`.**
Absent, it deletes the whole transcript; present, it deletes one track's, so a
single bad microphone can be re-run without discarding every other track's work
alongside it. `N` is zero-based, as track numbers are everywhere in this API,
and a negative or non-numeric one is `400` `invalid track`.

#### Searching transcripts

`GET /library/search` is the one route in this section with a query string
worth writing down. It needs an `admin` token or a session — see the denied
list above — and `q` is required; omitting it is a `400` naming the empty
query, not an empty result set.

| Parameter | Effect |
|---|---|
| `q` | **Required.** The search text |
| `prefix` | Treat the last word as a prefix, for search-as-you-type |
| `raw` | Pass `q` to the FTS engine as written, operators and all, instead of quoting it |
| `speaker` | One speaker label, as `GET /library` lists them |
| `recordingId` | Confine the search to one recording |
| `sessionId` | Confine it to one session |
| `track` | Confine it to one track, zero-based |
| `since`, `until` | Bound the time range. `2006-01-02` or full RFC3339 |
| `order` | `relevance` (the default), `time` (oldest first) or `recent` (newest first). Anything else is a `400` naming the three |
| `limit`, `offset` | The page. The store clamps `limit`; a negative `offset` is a `400` |
| `context` | How many segments either side of a hit to return |
| `snippetTokens` | How long the highlighted snippet is |

`prefix`, `raw` and every other boolean here accept `1`, `true`, `yes` or `on`;
anything else, including `0` and `false`, reads as off.

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "https://host:8080/api/v1/library/search?q=sponsor&recordingId=42&order=recent&limit=20"
```

An invalid `recordingId`, `sessionId`, `track`, `since`, `until` or any of the
integer parameters is a `400` naming the one that did not parse, rather than a
result set quietly computed without it.

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

`GET /clips`, `POST /clips` and `PUT /clips/buffer` are programme-scoped — the
buffer belongs to one source — so on an install with more than one they need
`?source=<id>`. See [Conventions](#conventions). `DELETE /clips/{name}` and
`GET /clips/{name}/download` are not — a clip already exists by name, and the
name is the whole address.

### Jobs

| Method | Path |
|---|---|
| `GET` | `/jobs`, `/jobs/overview` |
| `GET` `PUT` | `/jobs/policy` |
| `POST` | `/jobs/pause`, `/jobs/resume`, `/jobs/purge` |
| `GET` `DELETE` | `/jobs/{id}` |
| `POST` | `/jobs/{id}/cancel`, `/retry`, `/release` |

`GET /jobs` takes five filters, all query parameters:

| Parameter | Effect |
|---|---|
| `state` | One of `queued`, `running`, `done`, `failed`, `cancelled`, `deferred` — or `active`, which expands to `queued,running,deferred` |
| `kind` | The job kind |
| `target` | The job's target string, matched exactly |
| `recordingId` | The same thing for a recording, spelt the way a client already has it. Translated here to the recording target, so only one place knows how one is written |
| `limit` | How many rows to return |

`state` and `kind` are both repeatable **and** comma-separated, so
`?state=queued&state=running` and `?state=queued,running` are the same request.

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "https://host:8080/api/v1/jobs?state=active&limit=50"
```

`active` is the filter the console's own page opens with. It is spelt out on
the server rather than in the client because two definitions of "active" drift.

**An unrecognised `state` is a `400` — `unknown job state "quued"` — not an
empty list**, and so is a negative `limit` or a `recordingId` that is not a
positive integer. "No jobs match" and "your filter is misspelt" look identical
in a table, so the server refuses to let them.

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

**`POST /hooks` and `PUT /hooks/{id}` answer `400` for a non-public URL.** A URL
resolving to loopback, link-local (`169.254.169.254` included), RFC1918, RFC6598
or IPv6 ULA is refused, naming the hook:
`hook "x" targets a non-public address; set allowPrivateTarget to permit a
self-hosted endpoint on purpose`. Send `"allowPrivateTarget": true` in the same
body to mean it — it defaults to `false` and is read back with the hook. The
same check runs again at dial time on every delivery, so a name that changes its
answer later is refused then. See [HOOKS.md](HOOKS.md#private-and-lan-endpoints-allowprivatetarget).

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
| `POST` | `/platforms/credentials/{platform}/device`, `/platforms/credentials/{platform}/device/poll` |
| `GET` | `/platforms/accounts`, `/platforms/accounts/{id}/stats`, `/destinations/{id}/facebook/stream-health` |
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

#### Connecting an account with no callback URL

`/platforms/credentials/{platform}/device` is the **device code flow**: the way
to connect an account from a box no platform can redirect back to. The ordinary
`/oauth/{platform}/start` flow needs a redirect URI the platform will accept, and
a server reached as `https://192.168.1.50` or on a self-signed certificate has
none. This one removes the callback from the problem.

`POST …/device` asks the platform for a code and answers with `userCode`,
`verificationUri`, `expiresAt`, `intervalSeconds` and an opaque `handle`. The
operator types the code at the platform's own page — on a phone, on anything with
a browser — while the server holds the device code that redeems the token. **The
device code is never sent to the client.** The handle is what the client polls
with, exactly as the `state` parameter names a pending authorization-code flow.

`POST …/device/poll`, body `{"handle": "…"}`, answers `200` with a `state` of:

| `state` | meaning |
|---|---|
| `pending` | the operator has not finished yet. Wait `retryInSeconds` and ask again. **Not an error.** |
| `connected` | the account is stored and is in `account`. Stop polling. |
| `expired` | the code was used, timed out, or the server no longer holds the handle. Stop polling and start again. |

Only a real transport failure — the platform is down, rate-limiting, or refused
the token for an unclassified reason — leaves as a `502`, and the flow survives
one so a hiccup does not cost the operator their code.

The interval is enforced **on the server**: a poll that arrives early is answered
`pending` without a request leaving the process, because a client polling faster
than the platform asked spends the operator's whole app's rate limit mid-connect.

Exactly one platform offers this today, and the guide's `deviceFlow` field says
which — read it rather than hard-coding a name. A platform without it is refused
with a `400` naming the platform. `internal/oauth/device.go` records why the
other three are absent, and the three reasons differ.

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

**Every moderation route is addressed by query parameters, not by path
segments or a body.** A message id is an opaque platform-issued string and an
account ref is whatever the platform calls an account; neither survives a path
segment reliably.

| Route | Parameters |
|---|---|
| `DELETE /chat/messages` | `platform`, `id` (both required), `account` |
| `POST /chat/messages/hide` | `platform`, `id` (both required), `account`, `scope`, `hidden` |
| `POST /chat/bans` | `platform`, `userId` (both required), `account`, `seconds`, `reason` |
| `DELETE /chat/bans` | `platform`, `userId` (both required), `account` |
| `PATCH /chat/settings` | `platform` (required), `account`, plus the rules in the JSON body |
| `GET /chat/users` | `platform`, `authorId` (both required), `limit` |
| `GET /chat/messages` | `platform`, `limit` |
| `GET /chat/search` | `q`, `platform`, `limit` |

A missing required parameter is a `400` saying which: `platform and id are
required`, `platform and userId are required`, `platform and authorId are
required`, `platform is required`. `account` is optional everywhere it appears
and names which connected account on that platform to act as, for an install
with more than one. `limit` defaults to `300` — roughly a screenful of
scrollback and the room to scroll up through it — and is clamped to `2000`; a
value that is zero, negative or not a number silently takes the default.

`DELETE /chat/messages` removes one message on the platform. The platform is
asked first and the local copy is only dropped once it agreed, because the
other order leaves a message deleted in polyemesis and still on every viewer's
screen — the exact failure the button exists to prevent.

**`POST /chat/messages/hide` is local by default, on every platform including
Facebook.** `scope` is the switch, and the default is the half that cannot
overreach:

| `scope` | What happens |
|---|---|
| omitted, or `local` | polyemesis stops showing the message. Works everywhere, because it asks nobody's permission — and **every viewer still sees it** |
| `platform` | The platform hides it from viewers. Only Facebook can, and only because its live chat is a comment thread with an `is_hidden` field |

Anything else is a `400`: `scope must be "local" (hide it here only) or
"platform" (hide it from viewers)`. The `200` says which happened in words, not
just a status — a local hide comes back with `"scope": "local"` and a `detail`
of `Hidden in polyemesis only. Everyone watching on facebook can still see this
message.` An operator who believes a local hide cleared their audience's
screens has been misled by their own tool, so the response refuses to let them.

A platform hide is the reversible one, and `hidden=false` is how it is
reversed:

```sh
curl -H "Authorization: Bearer $TOKEN" -X POST \
  "https://host:8080/api/v1/chat/messages/hide?platform=facebook&id=12345_67890&scope=platform&hidden=false"
```

Only the platform scope can be undone. A local hide is forgotten rather than
flagged, and restoring a platform hide does not bring the message back into
this pane either — polyemesis does not re-fetch what it has dropped.

`POST` and `DELETE /chat/bans` ban, time out, and lift either. **The duration
is `?seconds=`, a whole number of seconds, and it is the only unit on the
wire.** `?seconds=600` is ten minutes everywhere. A value that is not a
non-negative integer is a `400`: `seconds must be a whole number of seconds, or
omitted for a permanent ban`.

**Omitting `seconds`, or sending `0`, is a PERMANENT ban** — that is all three
platforms' own convention, so it is kept rather than invented around, but it
means a client computing a duration that comes out empty or zero issues a
permanent ban on a live channel rather than a short timeout. Guard the
arithmetic before the request, not after.

```sh
# ten-minute timeout
curl -H "Authorization: Bearer $TOKEN" -X POST \
  "https://host:8080/api/v1/chat/bans?platform=twitch&userId=123456&seconds=600&reason=spam"

# permanent
curl -H "Authorization: Bearer $TOKEN" -X POST \
  "https://host:8080/api/v1/chat/bans?platform=twitch&userId=123456"
```

Seconds rather than each platform's own unit because the platforms disagree:
YouTube and Twitch count seconds, Kick counts *minutes*, so a `600` passed
straight through would be ten minutes on two platforms and ten hours on the
third. Each adapter converts at the last moment and rounds **up**, because
truncating 30 seconds to zero minutes would reach Kick as a permanent ban.

The `200` reports the verb back — `{"status": "timed out", "scope": "10m0s"}`
for a bounded ban, `{"status": "banned", "scope": "permanent"}` otherwise —
because those are different things to have just done and the caller should not
have to infer which from the request it sent.

`PATCH /chat/settings` is Twitch's channel rules — slow mode, followers-only,
subscribers-only, no repeated messages. It is a `PATCH` with pointer fields all
the way down: an omitted field means "leave it alone", which is the only way to
express "turn slow mode on and touch nothing else" without switching
followers-only off as a side effect.

One deletion trap is worth stating because the platform's own API hides it:
`DELETE /helix/moderation/chat` with **no** `message_id` deletes every message in
the channel and returns success. polyemesis refuses an empty id before the URL
is built.

## WebSocket

`GET /ws` upgrades and then pushes status, audio levels, process logs, loudness
reports and chat as they happen. It is the same data the polling routes return —
use it when you want changes rather than snapshots.

The upgrade is programme-scoped like the polling routes it replaces: on an
install with more than one source, connect to `/api/v1/ws?source=<id>` or the
upgrade is refused `400` `source_required` before it happens. That scope covers
the opening burst — the snapshot the server assembles for the client — but not
the stream that follows: every engine publishes onto the same socket, so frames
produced by other programmes arrive on it too. They are source-tagged, each
`status` frame carrying `source.id` and `source.name`, so a client can tell them
apart on `status.source.id`. What a UI cannot do is keep one status series and
one bitrate series for the socket — those get redrawn from whichever engine
spoke last — which is why a multi-source grid polls `GET /previews` instead of
deriving one from this feed.

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
