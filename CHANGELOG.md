# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

## [0.7.0] — 2026-08-11

### Security

- **The route that decides what your ingest pulls had no upload check at all.**
  Saving a pull source that names an upload this server was never able to
  inspect has been refused since #201 — but only on `PUT /settings`, and the
  engine does not read its ingest from there. `engine.effectiveSettings` does
  `settings.Ingest = src.Ingest`, so the source ROW is what the engine's FFmpeg
  opens, and `POST /sources` and `PUT /sources/{id}` accepted such a URL with a
  `201` and a `200`. That is the route the Sources page writes through, and it
  is the only route a second programme has ever had. Both now refuse it, with
  the same scoping the settings gate uses: what the save introduces, never state
  you inherited. ([#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **And the check in front of all three routes was reading the URL, not the
  file.** It matched the exact spelling the Library hands you and split it at the
  first `/`, while the engine resolves the same URL through `filepath.Join` — so
  `file://uploads/./show.ts`, `file://uploads//show.ts` and
  `file://./uploads/show.ts` all opened the identical file and none of them was
  checked. One typed dot was the whole bypass, on the gate #201 shipped as well
  as on the two above, and the source card stayed silent about it too. The check
  now asks the engine what path a URL resolves to instead of re-reading the
  format, so every way of writing one file gets one answer.
  ([#201](https://github.com/rainmanjam/polyemesis/issues/201),
  [#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **A source that was already pulling from an unchecked file now says so.** The
  gate above cannot see a URL saved before it existed, or one whose upload was
  downgraded underneath it, and there is no second gate downstream the way a
  playlist item has one. The source card now carries the server's own sentence,
  naming the file and what is wrong with it — and which of the two things is
  wrong, because they have different remedies: a file nothing could inspect is
  worth uploading again, and a file that *was* inspected and refused is not, so
  that one says to point the source somewhere else instead. It reports rather
  than refuses, deliberately: a missing inspection is a fact about this server —
  no `ffprobe`, an inspection cut short — and never about your file, so on an
  install without `ffprobe` a fail-closed re-check would take every `file://`
  ingest off air at once, at whatever hour the supervisor next respawned. That
  reasoning does not extend to a refusal, which *is* a fact about the file — see
  the entry below, which is where that half was settled. The field is on
  `GET /sources`, so a monitoring script sees it too.
  ([#255](https://github.com/rainmanjam/polyemesis/issues/255),
  [#264](https://github.com/rainmanjam/polyemesis/issues/264))

- **A source pulling from a file this server inspected and rejected is now
  taken off air.** The warning above is kept for an upload nothing could
  inspect, and only for that: a missing inspection is a fact about this server,
  so refusing on it would stop every `file://` ingest on a box without
  `ffprobe`. A *refusal* cannot be that — it exists only where an inspection ran
  and read the bytes — so an inherited pull source naming one is re-checked on
  every engine reconcile and its ingest is not started, primary and standby
  alike. The card still names the file and says why, and the reconcile records
  the same sentence, so a save made afterwards answers with it rather than
  leaving you a stopped programme and a log line. Nothing writes that verdict
  yet; #202's re-verify job will be the first.
  ([#255](https://github.com/rainmanjam/polyemesis/issues/255))

- **A busy server stopped inspecting uploads, and the caller chose when.** The
  four-slot semaphore that bounds concurrent `ffprobe` children waited for its
  slot *inside* the probe's own 30-second deadline, so a queued upload could
  spend the whole budget waiting and be stored with the verdict "the inspection
  was cut short". Nobody had to disconnect to reach that state any more — eleven
  more uploads did it. The wait now runs on the request's context and the
  deadline starts when the probe does, so a busy machine makes an upload slow
  rather than unchecked. ([#216](https://github.com/rainmanjam/polyemesis/issues/216))

- **A rejected upload told you where the server keeps its files.** `ffprobe`
  names its input in front of nearly everything it prints, and the `400` body
  passed those words through verbatim — the data directory and the internal
  `.partial-` name included. The path is now replaced with the name the file
  would have been given, at the handler egress, and the rest of the sentence is
  kept: `moov atom not found` is what tells an operator their download was
  truncated. ([#181](https://github.com/rainmanjam/polyemesis/issues/181))

- **Two outbound payload egresses were absent from the coverage ledger.** The
  webhook `POST` and the alert `POST` send payloads outward with no principal,
  which is the same shape as the retained MQTT topic the ledger already carries,
  and neither was listed — so nothing read their bytes. Both are now inspected
  by a proof that reads the real request off a real socket on a server whose
  every credential column holds a sentinel. The `:80` redirect listener is
  recorded as emitted-and-uninspected rather than left outside the ledger.
  ([#169](https://github.com/rainmanjam/polyemesis/issues/169))

- **`GET /assets/` returned a directory listing of the whole UI bundle, to
  anyone.** The SPA handler decided whether a request named a real asset by
  opening it -- and **opening a directory succeeds**, so the request was handed
  to `http.FileServer`, which answers a directory with a generated index page.
  An anonymous caller received the complete inventory of fingerprinted chunk
  names, which name product areas and reveal which features a deployment was
  built with.

  No credentials, no user data and no configuration were exposed; what leaked is
  build layout. A directory now falls through to the SPA fallback, which is how
  every other unknown path is answered.

  It survived this long because CI's Go job does not run `npm run build`, so the
  tests ran against an empty `dist` -- and against an empty filesystem the fixed
  and unfixed handlers are byte-identical. The bug was not merely untested, it
  was unwritable as a test until the handler was made to accept an arbitrary
  filesystem.

- **A `file://` pull now pins the input demuxer to the file protocol.** The
  engine's pull input carries `-protocol_whitelist file`, the same pin
  `ProbeFile` already used, so what an input demuxer may open is bounded by the
  argv rather than by whatever this FFmpeg build happens to enable.

  **This closes nothing observable today and is recorded as hardening, not as a
  fix.** Measured against FFmpeg 8.1.2: an ffconcat script naming `http://…` is
  refused identically with and without the flag, and one naming a sibling file
  is still resolved with it on. The substitution hole is closed by the format
  allowlist, which is not a flag and lives elsewhere.

- **Your MQTT broker password was echoed back in a validation error, and
  logged every five seconds.** A broker URL written with the credential inline
  — `mqtt://user:password@host:1883` — reached two places that repeated the URL
  verbatim.

  `MQTTSettings.problems()` builds the messages `Settings.Validate` joins into
  the **`400` body a settings save returns**, so the password came back over the
  wire to whoever submitted it. `parseBroker` wrapped the parse failure into the
  connection error, which the reconnect loop wrote to the log as an **`Error`
  line on every retry**.

  Two separate branches leaked, and the guard meant to prevent exactly this
  reached neither. `mqtt://user:pw@` parses cleanly and has no host, so the
  no-host message — which echoed the whole URL — fired before the credentials
  check ever ran. And `mqtt://user:pw@ho st:1883` does not parse at all, so the
  credentials check *could* not run: `url.Error.Error()` renders as
  `parse "<the whole URL>": <reason>`, carrying the password inside the wrapped
  error with no string concatenation anywhere in our code.

  Both messages now state the fault without repeating the input, and the
  credentials check runs first where a URL parses at all.

  **If you have ever saved a broker URL with a password in it, rotate that
  broker credential.** Upgrading stops new copies being written; it cannot
  remove the copies already in your logs, or in whatever collected those `400`
  responses.

- **A pull or destination password containing `@`, `/`, `!`, `#` or `%` was
  rendered verbatim in `GET /processes`, which a `read` token can reach.** The
  scrubber that removes credentials from a rendered FFmpeg command line is a
  substring replacement over the argv the process was actually handed. It was
  collecting the password through Go's URL parser, which **decodes** it — so for
  `rtsp://user:p%40ssw0rd%21@cam/stream` it held `p@ssw0rd!` and looked for that
  in a command line containing `p%40ssw0rd%21`. It matched nothing and masked
  nothing.

  Only passwords needing percent-encoding were affected; the username, the path
  and plainly-spelled passwords were always scrubbed. Both the pull side and the
  destination side had it, through separate code paths.

  **If a pull source or destination of yours uses a password with one of those
  characters, treat it as exposed to anyone who held a `read` token and rotate
  it.**

### Added

- **A raw `.h264`, `.hevc` or `.mpegvideo` dump can go straight into the
  Library.** These files carry no container, so nothing in them says how long
  they are, and the upload gate refused them: *"polyemesis cannot work out how
  long this file is — re-save it as MP4 or MPEG-TS and upload it again."* A real
  remedy, and manual work the product can do. polyemesis now **counts** the
  length instead, by decoding the file once, and accepts it. Measured on a
  10-minute 720p dump: 2.8 seconds. It runs inside the inspection budget an
  upload already has, so nothing new can make an upload slower than it could be
  before; a file too long to count inside that budget gets the old refusal, with
  the old remedy. Your file also keeps its extension now instead of being stored
  as `.bin` — for a raw stream that is the only hint FFmpeg has about how to read
  it.

  **The Library says which of the two it is.** A duration a container declared
  and a duration polyemesis counted are not the same claim: the first was written
  down by whatever made the file, the second is every frame we decoded times the
  frame rate the encoder declared in the bitstream, and a raw stream holds
  nothing to check that rate against. So `durationSource` sits beside
  `durationSeconds` and says `declared` or `counted`, rather than the two being
  indistinguishable once the number is in the field.
  ([#218](https://github.com/rainmanjam/polyemesis/issues/218))

- **An upload can now be recorded as *inspected and refused*, which is a thing
  the server previously had no way to write down.** The record beside every
  upload carried one boolean, so it had two states — accepted, and stored
  without being inspected — and the product needed three. A refusal was
  therefore never stored at all: the upload handler answers `400` and throws
  the staged bytes away, which works exactly once, at upload time, while
  nothing references the file. It does not work for anything that inspects an
  upload *later*: by then the file is published, `DELETE /api/v1/media/{name}`
  answers `409` while a playlist item names it, and the only state available to
  record the refusal in was the one that says "nobody read this" — which every
  consumer answers by telling the operator to upload the same bytes again, for
  a file that will be refused identically.

  `GET /api/v1/media` now carries `outcome` on every row, always present, with
  four values: `verified`, `unverified`, `refused`, and `unrecorded`. The fourth
  is not stored anywhere — it is what the listing says when there is no record
  beside the file, which is every upload an install made before verdicts
  existed, and it stays distinct from every recorded state because refusing
  those would strand media an operator has had for a year. The Library shows
  **Refused** rather than **Not checked** for the new state, the playlist editor
  does not offer it, and both settings validators refuse it with a sentence that
  does *not* say "upload it again".

  `verified` keeps its exact meaning and is still written to disk, because the
  sidecar format exists in every install and a format change has two directions
  in time: an older binary reading one of today's records — a rollback, or a
  second process during an upgrade — sees `verified: false` with a reason and
  refuses the file, which is the wrong label but the safe answer. An `outcome`
  this build does not recognise falls back to the same field rather than being
  trusted, and a record that claims a pass in one field while denying it in the
  other is refused outright.

  Nothing writes `refused` yet. The re-verify job is
  [#202](https://github.com/rainmanjam/polyemesis/issues/202); this is the state
  it needs to exist before it can be built, because a re-verify that records its
  findings as "not checked" would be worse than no re-verify at all.

- **A destination can now forward its audio bit-for-bit.** Set `copy` on a
  destination's audio block — `-c:a copy`, no decode, no mix, no encoder — so an
  archive or contribution feed carries the same bits your encoder sent us.
  Available in the destination dialog.

  It is called **copy** and not "passthrough" on purpose: passthrough already
  means a video rendition at the ingest's own resolution, and reusing the word
  would make "a passthrough destination" ambiguous in exactly the conversations
  where it matters.

  Copy still **selects**. The compiled routing profile decides which tracks go
  out and the role policy still removes excluded ones, so the DMCA switch keeps
  working; what you give up is everything the mix stage does to the samples.
  Because of that, a destination that sets `copy` alongside mix settings the
  copy path cannot honour is **refused, naming each setting individually**,
  rather than accepted with the settings silently ignored. A form that says one
  thing while the stream does another is the worse outcome.

- **Staging and rolling back the server binary now have endpoints.**
  `GET /api/v1/upgrade/plan` reports what an upgrade would do and is a read.
  `POST /api/v1/upgrade/stage` and `POST /api/v1/upgrade/rollback` perform it
  and are **session-only** — deliberately out of reach of an API token of either
  scope, because a leaked token that can replace the server's own binary is a
  different category of problem from one that can read a stream key.

### Changed

- **BREAKING: a `read` API token gets metadata, not content.** Thirteen routes
  now answer `403` to a `read`-scoped token that previously served them: the
  five media and export downloads, both transcript endpoints, and
  `GET /library/search`.

  Search is on that list and it is the one worth reading twice, because it looks
  like a metadata query and is not. `db.TranscriptHit` carries the segment
  `text`, its neighbouring `context` and the `speaker`, so a token that iterated
  common words would rebuild whole transcripts without ever requesting a route
  with `transcript` in its path. The line is drawn at what the bytes are, not at
  what the URL says.

  Listing is unaffected: a `read` token still sees recordings, clips, stems and
  sessions, their durations, sizes and status, and whether a transcript exists.
  `GET /library` still returns the bare list of speaker labels — who appears,
  rather than what was said. **If you have automation that downloads recordings
  or reads transcripts with an API token, it needs an `admin` token now**; the
  route works, the scope does not.

  **Every token you already have becomes `admin` when you upgrade.** The
  migration is `ALTER TABLE api_tokens ADD COLUMN scope TEXT NOT NULL DEFAULT
  'admin'`, so existing automation keeps working exactly as it did and nothing
  breaks on restart. The consequence worth stating plainly is the other
  direction: **nothing is restricted until you act.** The `read` scope is opt-in
  — a monitoring script does not become read-only by upgrading, and if you want
  it to be, issue it a new `read` token and revoke the old one.

- **BREAKING: an upload that no probe could read is stored but marked, and some
  formats are refused outright.** Uploads now carry a verdict record, so a file
  that was accepted without being inspected — a client disconnect mid-probe, no
  `ffprobe` on the box — is distinguishable from one that passed, and playlists
  refuse to reference it. Files uploaded before this exist as "no record", which
  is deliberately not the same as "unverified": your existing library is not
  stranded.

  Raw elementary streams (`.h264`, `.hevc`, `.mpegvideo` dumps) are now refused
  at upload with a message telling you to remux. They report no duration, so the
  old behaviour was to accept them and then fail the normalise job permanently —
  a playlist item that could never go on air. Refusing at the door is the same
  answer given earlier and with a remedy. See #218 if you need them supported.

- **BREAKING: a job-state conflict answers `409`, and a running job cannot be
  deleted.** `POST /jobs/{id}/cancel` and `/retry` against a job in a state that
  does not permit it returned `500`; they now return `409` with a sentence.
  `DELETE /jobs/{id}` on a RUNNING job is refused rather than removing the row
  and orphaning its worker.

- **`/hls/*` is session-only.** The dashboard preview playlist no longer accepts
  a bearer token of either scope, and the routes answer `GET` and `HEAD` only.
  If you point a player, a probe or a script at an HLS URL from outside a
  browser session, it stops working.

- **Revoking an API token now closes its open WebSocket, and changing the
  account password closes the operator's.** A socket used to keep the principal
  it was opened with for ever, so revocation — the only lever you have after a
  leak — did not reach a live connection.

### Fixed

- **A pull source could name an upload this server was never allowed to
  inspect.** An upload's `pullUrl` is offered copyable in the Library, and
  pasting it into **Settings → Ingest → Pull** handed the path to the engine's
  own FFmpeg — which is not the inspection path, and carries neither the format
  allowlist nor the protocol pin. So a file whose inspection a dropped
  connection cut short could be routed to air with nothing having read a byte
  of it.

  Saving a pull source — primary or backup — that names an upload **recorded as
  unchecked** is now refused, with the file and the reason in the message. It is
  the same test a playlist item already gets, scoped the same way: only a URL
  this save *changes* is checked, so an unrelated settings change is never
  refused over a source configured before the gate existed. An upload with no
  record at all — every file stored before inspection existed — is still
  allowed, because refusing those would strand media an operator has had for a
  year.

- **A killed upload left bytes on your disk that nothing would ever remove.**
  Nothing in the product swept the uploads directory. A staged file stranded by
  a process that died mid-transfer, or by a cleanup that failed, stayed
  forever — invisible and unreferenceable, but occupying up to 8 GiB of the
  volume the database, the recorder and the HLS preview share, with the
  free-space floor unaware of it and nothing anywhere reporting the total. The
  same for an inspection record whose upload was removed out of band.

  Startup now clears both, once, before anything can accept an upload, and
  **logs what it removed and how much space came back** — the boot after the
  crash is the one where that line matters. Only leftovers older than an hour
  are touched, so a sweep can never race a transfer that is still arriving.

- **An upload was briefly listable as an empty file while it was being
  published.** Finalising an upload reserved its final name with an exclusive
  create, which is what makes two uploads drawing one name a loud failure
  instead of one operator's bytes silently replacing another's. But the
  reservation was a zero-byte file *under that final name*, so until the bytes
  were renamed into place `GET /api/v1/media` showed an empty row with a working
  `pullUrl`, and a settings save would accept it as a playlist item. The window
  was a file write wide on every platform and tens of milliseconds on Windows.
  The reservation now uses a name the listing refuses, so nothing is ever
  visible under the final name except the finished file — and a name that is
  genuinely already taken is still refused.

- **A media record could be written for a name no upload has.** The call that
  records what was found in a file checked only that the name had no path
  separator, so any other string wrote a sidecar that nothing in the product
  ever reads or removes. No caller could produce one, which is exactly why the
  contract was narrowed now: it refuses a name that is not a listable upload,
  and a name with no file beside it.

- **A destination whose FFmpeg had already died could be reported as running
  indefinitely.** The supervisor waited for a child's output pipes to reach
  end-of-file before reaping the child itself — and a pipe reaches end-of-file
  when the *last* writer closes it, not when the process you started exits.
  FFmpeg spawning a helper that outlives it, or anything the child forks and
  does not wait for, inherits those pipes and holds them open. The supervisor
  was then waiting on a process it never started: the destination stayed green
  on the dashboard, the restart policy never fired, and a stop of that
  destination was bounded by the lifetime of a grandchild rather than by any
  timeout.

  The child is now reaped on its own, and the drain that captures its stderr is
  bounded afterwards. The tail of stderr that becomes a destination's error
  message is unchanged on the ordinary path.

- **Stopping a destination cost a sleeping goroutine per stop, for eight
  seconds each.** The escalation that kills a child which has not heeded the
  shutdown signal waited out the full grace period on every stop, including the
  overwhelming majority that finish in milliseconds. Stopping forty
  destinations left forty goroutines parked, each holding a reference to a
  process it still intended to kill. The escalation now returns as soon as the
  child is reaped.

- **`POST /destinations/{id}/stop` said `"stopped"` whether or not the process
  actually stopped.** The supervisor reports the same state on both outcomes:
  the one where the child was reaped, and the one where the shutdown deadline
  expired, `SIGKILL` was sent, and nothing waited to see whether it worked. The
  second means a process that may still be running is still holding — and still
  publishing to — the relay port and the stream the response has just declared
  free.

  A stop now also answers `reaped`, plus a `warning` saying what is uncertain
  when it is `false`. Nothing about the `state` field has changed, so existing
  callers are unaffected.

- **A `read` token could read a pull URL's credential from `GET /sources`.** The
  masking ran, and masked the wrong part: it blanked only the last path segment
  and only for `rtmp`/`rtsp`/`srt`-like schemes, so an HLS pull URL over `https`
  came back untouched and one with the credential mid-path had its *filename*
  masked instead. Redaction for a read-scoped reader now masks every path
  segment and every query parameter, on every scheme. A `read` token sees the
  host and nothing else; a session and an `admin` token are unaffected.

- **A `read` API token no longer reaches the live media of a stream you have not
  published.** `read` means metadata, not content, and live playout is content.

  The three playout routes sit outside every authenticated group by necessity —
  a viewer has no account — so the scope check runs per request in the handler,
  and it was asking the wrong question: it resolved the caller and then threw
  the principal away, so any valid bearer was treated as the operator previewing
  their own private stream. A `read` token is now treated exactly as an
  anonymous viewer on `/playout/*`, `/playout/public` and `/playout/poster.jpg`
  — the same status, the same body, the same headers.

  Two consequences worth knowing before you upgrade. On a stream with
  `Public: false`, a `read` token now gets `404` where it used to get the media.
  On a published stream protected by the playback token, a `read` token that
  does **not** present that playback token now gets the `401` challenge instead
  of being served; presenting it in `?t=`, the header, the cookie or as a basic
  password works as it always did. Anonymous viewers, the signed-in console and
  `admin` tokens are unaffected, and an open stream is unaffected for everyone.

- **The "Partly bound" badge on a source card now appears only when there is
  something to fix.** A wildcard SRT listener binds one socket per address
  family and survives one of them failing, which on an IPv4-only host — a
  container with IPv6 disabled — meant a permanent orange badge on a perfectly
  healthy install. The badge now distinguishes an address family this machine
  does not have from one it has and could not bind, and only warns about the
  second. The log line is unchanged and still records both, because whoever is
  working out why an encoder will not connect needs the first one too.

- **`POST /routing/presets/{preset}` now applies the defaults it advertises.**
  A request with no body used to run on Go's zero value for every track index
  rather than on the OBS-convention defaults `GET /routing/presets` publishes
  under `defaults`, so a bodyless `mic-only` compiled `[0:a:0]` — the full mix —
  where the catalogue two routes away said track 3. Both routes answered `200`
  and neither disagreed out loud.

  **Behaviour change worth knowing before you upgrade.** A bodyless apply of
  `mic-only` now selects track 3 instead of track 1, and `clean-feed` track 2
  instead of track 1. Anything that sends a body with a Content-Length —
  including the console, which always sends the full option set — is unaffected.
  A body is still a FULL REPLACEMENT rather than a patch over the defaults, so a
  partial body means exactly what it always meant: an omitted field is zero.

  Two smaller changes fall out of the same fix, and the first paragraph used to
  imply neither happened. A **chunked** body now takes effect: it reports
  `Content-Length: -1` whatever it carries, so the old gate discarded it and
  applied the defaults. And a **whitespace-only** body is now treated as absent
  (200, defaults) where it used to be `400 invalid request body: EOF` — it is
  not a "full replacement", because there is nothing in it to replace with.

- **Three more values a `read` token could read.** The automod model endpoint
  when it carries an `?api_key=` in the query string, and a destination's
  `extraInputArgs` / `extraOutputArgs` — which are the resolved FFmpeg argv, the
  same bytes `GET /destinations/{id}/expert` already refuses that token for.

- **A `kind: file` destination no longer loses its filename for a `read`
  token.** That field is a filename rather than a URL, and running it through
  the URL masker replaced the whole thing with `[redacted]`.

- **Redacted fields keep their JSON key.** Four fields carry `omitempty`, so
  blanking them removed them from the document entirely and a `read` token's
  response was a different shape from an admin's. They now come back as
  `[redacted]`.

- **`Vary` on principal-dependent responses now names `Cookie` as well as
  `Authorization`.** The signed-in operator — the principal that receives the
  unredacted body — arrives in a cookie, so a shared cache keyed on
  `Authorization` alone filed their response under the same key as every
  anonymous caller's.

- **Deleting a completed clip export left its file on disk for ever.** All three
  delete paths -- the single-job delete, the bulk purge and the unattended
  scheduled sweep -- removed the job row and left the exported media behind,
  unreferenced and invisible, occupying the volume the database and recorder
  share. The file now goes with the row.

- **Staging or rolling back the binary now raises an audit event.** Both were
  logged and neither was audited, so the one question an operator asks after an
  unexpected version change -- *was that me?* -- had no answer in the place they
  would look.

- **Masked URL segments came back percent-encoded.** A redacted path segment in
  a `/sources` or `/settings` body rendered as `%5Bredacted%5D` rather than
  `[redacted]`, because the mask is written into the URL and `url.String`
  escapes its brackets. Cosmetic, but it landed in the one place a reader is
  least equipped to tell a deliberate omission from a bug.

- **The startup banner no longer prints an ingest port in pull mode.** Pull
  dials out and has no inbound port, so `ingest      pull (port 6000)` read as
  "point the encoder at 6000" — an instruction that can never work, and one
  that sends you to your firewall to debug a port that was never in the path.
  It now reads `pull (dials out; no inbound port)`.

### Testing

- **Two guards that read `Dashboard.tsx` as text are now browser tests that
  drive it.** `internal/oauth/composer_tags_drift_test.go` proved that the
  substring `tags` appeared within 400 characters of the push `fetch`, and that
  `withCompliance.length > 0` was written somewhere in the file. Neither can
  tell whether the component compiles, mounts, or does the thing it names — the
  #107 defect, one package over. They are replaced by `ui/e2e/go-live-composer.spec.ts`,
  which types into the tags input, clicks Push and reads the body off the wire,
  and which drives the compliance notice and the Push button through both the
  stored and not-stored cases. The stated blocker was re-tested rather than
  assumed: the composer decides everything from `GET /metadata`, so a stubbed
  response reaches every branch with the real component and no OAuth fixture.
  The Go file is deleted. Browser suite 92 → 97.
  ([#259](https://github.com/rainmanjam/polyemesis/issues/259))

## [0.6.0] — 2026-08-09

An operator-facing release: the server now tells you an update exists and what a
restart would cost, a rendition stopped doing work it threw away, and a failover
respawn stopped ignoring its own backoff.

### Added

- **The server tells you when there is a newer release, and what stopping would
  interrupt.** A banner in the UI, translated across all fifteen locales, and a
  version endpoint behind it.

  The second half is the part that matters. polyemesis is not a tool you run and
  close; it is the thing carrying a broadcast, and restarting it drops every
  destination mid-stream. So the check reports what is **on air** — how many
  encoders are publishing, how many destinations are live, whether a recording is
  running, and which programmes those belong to — rather than a yes/no on whether
  an upgrade is allowed. "Cannot upgrade" is useless on its own; "3 destinations
  are live and a recording is running" lets an operator decide it is fine.

  A destination that is reconnecting counts as live, because from the desk it is
  mid-broadcast, and restarting underneath it turns a recoverable blip into a
  dropped show.

### Changed

- **A rendition that drops frames now drops them before scaling, not after.**
  `-r` decimates at the encoder, which is after the filter graph, so a 60→30
  rendition scaled all sixty frames of the source and then discarded half of
  them. Every one of those scales was work spent on a frame that never reached
  the encoder.

  Measured on a 6-core Haswell VPS, 4K60 → 1080p30 at veryfast/6000k: 2.13×
  realtime before, 2.49× after — **17% more headroom** on the case that needed it
  most.

### Fixed

- **A supervised child that had to be killed is no longer reported as one that
  stopped.** `Stop` gives a wedged process a deadline and then sends SIGKILL,
  but it returned at that point without waiting and recorded the fact only in a
  log line — and both outcomes then reported the same state, so nothing could
  tell "the child exited" from "the child was killed and may still be running".

  The failover tier is where that matters: it starts a replacement feed into the
  same hub the moment the teardown returns, so a child still alive and still
  writing is two publishers on one input. The condition is now surfaced on the
  tier and logged. The switch still proceeds — leaving the tier with no feed at
  all is worse than a seam — so this is a thing you are told about rather than a
  thing that blocks a switch.

- **The failover respawn backoff is measured from the feed's start, not from the
  decision to switch.** The timestamp recorded as a feed's start time was taken
  before a teardown that blocks for as long as the outgoing process needs to
  exit — up to twelve seconds. After a slow teardown the backoff interval had
  therefore already elapsed before the replacement had run for a single second,
  so a feed that failed immediately was respawned on every 500 ms sweep instead
  of backing off.

  Nothing else at that seam changed in this release. A second change was made
  alongside this one — moving `-output_ts_offset` from the switch decision to
  the feed's actual start, on the same reasoning — and was **reverted before
  release**. With it, the failover suite reported a backwards decode timestamp
  at a switch in 3 runs of 12; without it, 0 in 12, both before the change and
  after reverting it. A platform drops the connection on a backwards DTS, which
  is the failover tier failing at the one thing it exists for, so the
  configuration with none of them is the one that ships.

  Whether that change CREATED those steps or merely made existing ones large
  enough to cross the suite's one-millisecond threshold is not settled, and the
  distinction matters: one is a bug introduced, the other a bug revealed. Issue
  #126 stays open for it and the check now reports the size of every step,
  including the ones too small to fail on. What is settled is the direction, and
  the bug the reverted change described — a backwards step caused by a slow
  teardown — was never observed at all.

### Internal

- **Groundwork for in-place upgrades, not yet reachable.** A new `internal/upgrade`
  package works out how an install was made — container, systemd unit, or a
  binary someone runs themselves — and refuses to act where acting is useless or
  reckless. For the one case it can act on it stages a checksum-verified binary
  beside the running one, keeping the outgoing one for rollback, ordered so that
  every state the process can be killed into leaves a runnable box.

  Nothing calls it yet. There is no endpoint and no button, so this release
  changes no upgrade behaviour; it is listed here because it exists in the tree
  and because the next release is expected to wire it. Four adversarial review
  rounds went into it, which is the appropriate amount for code whose failure
  mode is a server with no runnable binary.

## [0.5.0] — 2026-08-08

The first release with the core review in it, and the version number is
deliberately 0.5.0 rather than 0.6.0.

0.5.0 was written up in this file on 2026-08-07 and then never tagged, so its
contents have never been in anyone's hands — a heading that looked released
above work that was not. Folding this release into it closes that rather than
shipping 0.6.0 over a hole in the version line, or retroactively tagging a
release nobody could have installed.

So everything below is one release: the four-reviewer core review worked end to
end, plus the two operator-visible changes the never-cut 0.5.0 had described.

### Added

- **`polyemesis -reset-admin`** — set a new admin password from the box the
  database lives on, for an operator who has shell access and no way in through
  the UI. Asks twice without echoing, signs out every existing session, and
  exits without binding a port, so it is safe to run against a live server. Pipe
  the password twice to script it.

  The password is deliberately **not** a flag: argv is visible in `ps` to every
  other user on the machine, lands in shell history, and appears in any audit log
  that records command lines.

  It also replaces advice nobody should follow. Deleting the row from the users
  table does restore first-run setup — `needsSetup` is just "the table is empty"
  — but `POST /api/v1/setup` is unauthenticated and the only guard on it is that
  an account already exists. Deleting the account removes that guard, so until
  setup is finished anyone who can reach the port can claim the install.
  `-reset-admin` never opens that window.

### Fixed

Nine more defects from the same core review, in the transport and concurrency
layers. Every one is the shape the whole review turned out to be: a protection
that is present in name and absent in effect.

- **An RTMP publisher for an SRT source is refused immediately again.** The
  readiness grace described further down this same release claimed in its own
  comment that it could not be used to hold connections open. It could: a target is registered for every
  source whatever its ingest mode, so any valid token for an SRT-mode source was
  found, enabled, and permanently not ready — the one verdict the grace waits
  on. Every connect burned the full six seconds, in parallel, for a state no
  amount of waiting could change. The listener now waits only where a subscriber
  is genuinely on its way, and caps waiters per publisher slot so one reconnect
  gets its grace while a flood does not multiply it.

- **A measured layout survives the encoder going quiet.** Roughly nine seconds
  into any outage the engine began reporting a real measured layout as the
  placeholder, because the check asked "is a stream arriving now" where it meant
  "has one ever been measured". The meters were torn down, the captioner rebuilt
  against an unknown source, and the routing preview stopped describing the graph
  its destinations were running. The stem plan had the same mistake from the
  other end: an encoder going quiet emptied it and restarted the recorder
  **without stems** mid-outage, then restarted it again when the probe returned.

- **A genuine data race in the relay's loss measurement.** `Deliver` runs on the
  SRT read loop, and a publisher takeover deliberately overlaps two of them —
  closing the incumbent's connection wakes its `Read`, which is not the same as
  it having finished. Two goroutines wrote the continuity counters and the
  send-error tally with nothing between them, corrupting the TSLost figure that
  the "UDP on loopback is defensible because it is measured" argument rests on.

- **A dead subscriber no longer counts as a reader.** A subscriber whose FFmpeg
  exited while nothing was publishing was never noticed — there are no writes to
  fail — so readiness kept reporting a closed socket as a live reader and
  admitted publishers into a stream nobody was reading.

- **A mid-stream cue point no longer replaces the cached metadata.** Every AMF0
  data message shared one setup slot, so a cue point evicted `onMetaData` and
  every subscriber attaching afterwards got the cue point replayed where its
  metadata should have been.

- **A source disabled between connect and publish is now refused.** The SRT
  publish callback re-checked the token and the pipeline but not whether the
  source was still enabled, so an operator's "off" did nothing until the session
  ended by itself.

- **Two recorders can no longer start on the same segment pattern**, and **two
  engines can no longer start for one source.** Free-space recovery reconciled
  the recorder without the lock every other caller takes, and the manager's
  engine sync had no such lock at all while being reached from several HTTP
  handlers — leaving a running engine that nothing held a reference to.

- **A destination can no longer report "running" while publishing nothing.** The
  primary feed's signature named what the failover tier was supposed to be rather
  than what the feed was reading, so a feed started during a hub swap matched the
  signature the next reconcile asked for and was left alone permanently: the hub
  carried zero bytes, every destination read healthy, and nothing raised an
  error.

### Changed

- Faster on the paths that run constantly: the relay fan-out takes no lock and
  allocates nothing per datagram, a held publisher is woken by its subscriber
  attaching instead of polling the database sixty times, and the status snapshot
  no longer scans its destination list twice per row.

  One optimisation was tried and **withdrawn**. Coalescing status pushes onto a
  150 ms window is a genuine saving, and it cost three broadcasts in ten: with it
  the failover suite handed a destination a backwards decode timestamp at a
  switch in 3 runs out of 10, measured against 0 in 10 without it. A platform
  drops the connection on a backwards DTS, which is the failover tier failing at
  the one thing it exists to do. See issue #126.

Earlier in the same review, four defects in the per-destination audio routing:

Four defects in the per-destination audio routing, all of them audible, all
found by a four-reviewer pass over the compiler and confirmed by measuring what
FFmpeg actually produced rather than by reading the filtergraph.

- **The downmix now reads the track's channel layout, not just its channel
  count.** Every 3-channel track was treated as 3.0 = FL FR FC. A 2.1 track is
  FL FR LFE, so its LFE was folded into both legs at −7.7 dB — precisely what
  the file promised in its header never happened. The same mistake dropped a
  real channel from `hexagonal` and `6.1(back)`, which have no LFE at the index
  where 5.1 and 7.1 keep theirs. Coefficients are now assigned by channel name
  from libavutil's own layout table, with the old count-keyed table kept as the
  fallback for a layout ffprobe could not name.

  **What you may notice.** The layouts that were already correct — mono, stereo,
  3.0, quad, 5.0, 5.1, 5.1(side), 6.1, 7.1 — compile to byte-identical graphs
  and their destinations do not restart. A destination fed a 2.1, 3.1, 4.1,
  hexagonal or 6.1(back) track will restart once on upgrade, and will sound
  different afterwards, because it was wrong before.

- **`auto` clip protection now looks at the gain, not only the track count.**
  The rule was "only summing across tracks can clip, so one track needs no
  limiter". But `pan` sums too, per output channel, and validation caps each
  cell at 2.0 and never the row. A one-track matrix with three cells at maximum
  gain on one leg compiled to `c0=2*c0+2*c2+2*c4` — six times full scale, hard
  clipping, with `auto` having decided no protection was needed. The track count
  keeps its say and the peak per-output gain gets one as well. This only ever
  adds a limiter: nothing that has one today loses it, and any profile peaking
  at or below unity compiles to the string it always did.

- **A wide track of unknown layout no longer sits off-centre.** The fallback
  splits even channels left and odd channels right, then normalised each leg by
  its own sum — so an odd channel count, which puts one more channel on the
  left, scaled the two sides by different divisors. Nine channels gave a
  permanent 1.94 dB image shift. Both legs are now divided by the same figure.

- **A matrix whose ingest has narrowed now says the level moved.** A saved 5.1
  matrix meeting a stereo ingest drops the cells addressing the missing channels
  and keeps coefficients that were scaled for the old width, leaving the
  destination 7.7 dB down. It warned about the channel numbers and never about
  the volume, which is the part anyone would actually notice. The coefficients
  are still the operator's to change — rescaling them silently would be the same
  category of mistake — but the drop is now stated in dB.

### Changed — the two from the never-cut 0.5.0

Written up on 2026-08-07 against a tag that was never pushed. Both are still
accurate and both ship here for the first time.

- **An RTMP publisher is now admitted only when something is subscribed to read
  it.** `Target.Ready` used to mean "an engine exists for this source and its
  mode is rtmp", which says a subscriber SHOULD exist, never that one does. A
  publisher whose ingest child never spawned, or was crash-looping, was admitted
  into a stream with no reader: the encoder goes green, the bytes are fanned out
  to nobody, and the operator has a healthy OBS and no output with nothing
  saying why. It is now asked of the listener directly. The same fix was applied
  to the failover standby, which had been a hardcoded `true` sitting directly
  below a comment describing the opposite contract.

  **What you may notice.** The ingest child carries no reconnect flags — it
  exits whenever its publisher does and is respawned on a 500 ms–5 s backoff —
  so an encoder reconnecting after a network blip arrives while nothing is
  subscribed. Rather than refuse it, the listener now HOLDS such a connection
  for up to 6 seconds and admits it if a subscriber attaches. Only that one
  verdict waits; an unknown key or a disabled source is still answered at once.

- **Destinations are not planned until the ingest layout has been measured.**
  Before a probe lands, the layout is a placeholder of six stereo tracks that
  exists so the routing editor has something to draw. Compiling a real routing
  graph against it fails in two ways, and the second is why this is a guard
  rather than a warning: a profile naming a track the stream lacks emits
  `[0:a:5]` and FFmpeg refuses to start — loud, and diagnosable — but the
  placeholder also claims two channels on every track, so a real 5.1 track
  compiles to `pan=stereo|c0=c0|c1=c1`, which is perfectly valid FFmpeg. The
  destination starts, stays up, and publishes front L/R only, with centre —
  where dialogue lives — discarded and no error anywhere.

- **A routing preview compiled from the placeholder now says so.** Every
  destination endpoint returns a compiled `filterComplex` so the editor can show
  the mix without a second round trip. Those compiled against an unmeasured
  layout are marked `routingProvisional`. They are flagged, never withheld:
  configuring a destination before going live is when most people configure them.

- **Both ingest ports bind by default, and both are published by default.**
  The RTMP listener used to bind only when some enabled source was configured
  for it, while SRT bound unconditionally. That was not a policy — it was two
  histories preserved side by side: `ffmpeg -listen 1` had held 1935 only while
  an RTMP source existed, and the SRT listener had always been up. The asymmetry
  showed on a fresh install that had chosen no ingest mode at all, which still
  opened 6000 while refusing to open 1935 on the grounds that nothing there
  spoke the protocol.
  It also disagreed with this project's own instructions: `docs/HARDWARE.md` and
  `docs/TROUBLESHOOTING.md` have always said to run
  `-p 6000:6000/udp -p 1935:1935`, so we documented publishing a port that might
  not be listening. datarhei Restreamer opens both, and so do we now.
  `install.sh` matches: it asks for an RTMP port the way it asks for an SRT one
  rather than a yes/no, defaults to publishing 1935, and takes `--rtmp-port 0`
  to decline it. **The port is the switch on both sides** — the server treats 0
  as off too — instead of a yes/no in the installer and a port in the settings
  meaning the same thing two different ways.
  What this adds is narrow: a host with **no firewall at all** now has 1935
  reachable, where the source list used to close it by accident. Everywhere else
  the ufw rule and the compose publish still decide, which is what they always
  claimed to do. Both listeners refuse an unknown token or key in constant time,
  both require the source to be ready before admitting anything, and a
  connection that says nothing dies on the handshake timeout.

### Fixed

Twelve defects in the work above, found across four independent review passes.
The ones with operator-visible consequences:

- A probe takes up to ten seconds and used to commit its result unconditionally.
  One in flight across an ingest restart or a mode change re-certified the
  previous transport's track layout under the new mode — and stamped it in a way
  that satisfied the guard permanently, so destinations compiled against a dead
  stream's layout until something else changed.
- The guard held every tier below it, not just destinations. Killing the primary
  encoder clears the probe state, so it fired for exactly the window the failover
  selector exists to cover: the selector stopped being reconciled and the late
  catch-up put a backwards decode timestamp in the output — the discontinuity a
  receiving platform drops the connection on.
- A destination could be left subscribed to a relay hub that was then closed
  underneath it. Closing a hub stops delivery without ending the process, so
  FFmpeg sat there running and receiving nothing: 76 seconds, zero bytes, no
  error. It reproduced about one run in two.
- A video-only source going idle tore down its silence tier, after which every
  destination on that source was torn down too, for as long as the encoder was
  quiet.
- A probe that could never succeed — a missing ffprobe, an unidentifiable
  stream — held every destination down and said nothing at all. It now says so.

### Installer

- **The installer now offers to serve HTTPS on 443, and opens it.** The port was
  asked for before the TLS mode was chosen, so an operator picked one without
  yet knowing they would be serving HTTPS at all — and the default 8080 gave a
  working but unlovely install where every link carried a port and nothing
  listened on the one people try first. Offered only when the port is still the
  untouched default; a port given on the command line or typed at the prompt is
  a decision and is left alone. The firewall rule follows the chosen port rather
  than opening 443 unconditionally, because a port nothing binds looks like
  working TLS and serves nothing.

- **`CAP_NET_BIND_SERVICE` is granted for any privileged port, not only for
  ACME.** It used to be gated on `tls.mode: acme`, which covered the `:80`
  challenge and missed the web UI itself: selfsigned is the DEFAULT choice, so
  an operator taking the 443 offer would have got a unit that could not bind the
  port the installer had just written into its own `ExecStart` — `bind:
  permission denied`, on a fresh install, from following the prompts.


### Testing

- The cross-platform smoke test now publishes over **E-RTMP and SRT** as well as
  injecting into the relay hub, and measures per-destination audio for each.
  Both publishers are pure Go — `gortmplib` and `datarhei/gosrt` — so the one-port
  listener, the readiness gate and multitrack FLV demux are exercised on macOS
  and Windows too, not only on the Linux acceptance suites. FFmpeg is left doing
  only what every build can do: muxing.
- The container suite gained two steps that publish routed audio to a real RTMP
  sink, from an E-RTMP ingest and from an SRT ingest, closing a coverage gap
  where each half had been proven and the combination never had.
- A destination that runs its whole life and writes nothing now fails the
  failover suite. It used to be a note, and a note is why the 76-second silent
  destination above stayed green.

## [0.4.0] — 2026-08-07

A minor bump. Nothing here breaks a stored config, but two behaviours change in
ways an existing install will notice: **ingest mode no longer has a default**, so
a fresh install asks rather than choosing; and **RTMP ingest is now addressed by
stream key** on one shared listener rather than by a port per source. Keys that
already worked keep working through a grandfather clause.

The headline is multi-source RTMP: how many programmes an install can carry no
longer depends on which protocol the encoder speaks. Alongside it, the audio
track ceiling goes from six to thirty-two, destinations gained per-platform
encoder guidance sourced from what each platform publishes, and the project got
a website.

The fixes are worth reading in full. Three of them — a data race in the new RTMP
listener, multitrack being unusable for any subscriber that joined late, and
every SRT install reporting its ingest as offline — were found by tests written
after the feature, which is the argument for writing them.


### Added

- **Chat search.** `GET /api/v1/chat/search?q=` matches a message's text or its
  author's name across the retained scrollback, newest first, optionally scoped
  to one platform. The pane gains a search box that replaces the live timeline
  while a query is active — results come from the database and are frozen at the
  moment of the query, so letting live messages append underneath them would
  present two different things as one list. Search is where an operator can
  wrongly conclude something did not happen, so the retention caveat renders on
  an empty result, not only on a full one.
- **Right-click and double-click moderation on a chat message.** Timeouts, delete
  and the user card, two seconds from reading a bad line to acting on it.
  Double-click is the same menu for pointers with no secondary button. It adds no
  capability the user card lacked; a permanent ban deliberately does *not* fire
  from the menu but opens the card, which confirms — the one irreversible action
  reachable from a right-click should not be a single click on a menu that
  appeared under the cursor.
- **Links out to the platform.** Twitch gets its real moderator viewer card in a
  separate window. YouTube, Kick and Facebook get a profile or channel link and
  say so, because none of them publishes a per-viewer chat history at any URL —
  a uniform "Open on <platform>" label would promise a moderator the same thing
  everywhere and deliver it only on Twitch.
- **Per-setting help.** An `(i)` popover beside a setting's label explains what it
  actually changes — 2.5× your round-trip time for SRT latency, why the free-space
  floor is the only limit that accounts for files polyemesis did not write. Click
  rather than hover, so it works on touch and under a screen reader, and the body
  is a catalogue key, so the explanation is translated too.
- **Multi-source RTMP.** RTMP ingest was `ffmpeg -listen 1` — a single-connection
  receiver that cannot demultiplex by path — so an install could carry exactly
  one RTMP source while SRT carried as many as you liked. That asymmetry was an
  artefact of the implementation rather than a decision. `internal/rtmpserver`
  is now one listener on one port for every source, addressed by the stream key
  in the publish URL, the same shape `internal/srtserver` already had. Publishers
  push to it and this install's own FFmpeg processes subscribe to it on the same
  socket. Media is never parsed: RTMP *messages* are relayed, so `-map 0 -c copy`
  downstream is untouched and Enhanced RTMP multitrack passes through without the
  server knowing what a track is. Existing keys keep working through a
  grandfather clause.
- **Thirty-two audio tracks, up from six.** `routing.MaxTracks` is 32, with a
  Σchannels ≤ 64 guard because FFmpeg's `amerge` caps there and the failure past
  it is a filter graph that will not build.
- **A marketing website.** `web/` — Astro, static, its own container image, with
  a build-time guard on the parts of it that have silently broken before.
- **Per-platform encoder guidance.** Each preset carries the resolution, bitrate
  and frame rate the platform itself publishes, with the source URL and the date
  it was read attached — both required fields, because a figure whose provenance
  is not on screen is indistinguishable from a guess. Cross-checked against OBS's
  own `services.json`.
- **An advanced section for customising a destination's encode.** A variant is
  a second encode seeded from a shared one, so an operator can have "the same
  tier but 4500 kbps for the constrained uplink" without editing the tier every
  other destination is on.
- **Alerts when a destination stops keeping up**, and security and configuration
  events for Slack and Discord.
- **An OBS acceptance suite.** `scripts/acceptance-obs-multitrack.sh` runs OBS
  headless in Docker and publishes into a real polyemesis, which is the only way
  to test OBS's own handshake and metadata rather than FFmpeg's.


### Changed

- **The application is translated.** Every page under `src/pages` now reads from
  the catalogue; a sweep for hard-coded prose returns nothing. The catalogue grew
  from 135 keys to 1,098 and all fifteen languages are complete.

  Previously only the nav shell and three widgets were extracted, so an operator
  who chose Deutsch got a German sidebar and an English application. The 135 keys
  that existed were complete in every language, which is exactly why the gap was
  easy to miss: the coverage looked like 100%.

  Extraction found prose in four shapes, each invisible to the check written for
  the one before it — JSX text, string props and toasts; ternaries such as
  `{busy ? "Pushing…" : "Push to platforms"}`; object-literal properties built
  from template literals; and eight module-scope tables. The last of those could
  never have called `useT()` at all, since hooks do not run at module scope, so
  each now holds a `TranslationKey` and is translated where it is rendered — a
  `Record<K, TranslationKey>` cannot hold a sentence, which the compiler enforces.
- **One word per locale for "rendition".** Ten of the fifteen catalogues disagreed
  with themselves across `nav.renditions`, `rend.title`, `sources.renditions` and
  `dash.renditions`. In Polish the sidebar read "Opcje jakości" while the page it
  opened was titled "Warianty", so following the link appeared to land elsewhere.
  Japanese, Korean and Dutch additionally used a term meaning "rendering
  settings"; a rendition is one video variant at a given size and bitrate, not a
  settings screen, and they now say so.
- `vitest` runs in CI alongside `tsc` and `oxlint`, covering the pure logic the
  browser suite cannot practically enumerate — platform link construction across
  five platforms, and the translation catalogues themselves.
- **Ingest mode is an explicit first-run choice, with no default.** The two
  options are not interchangeable and the difference is not recoverable by
  guessing: SRT carries every audio track, and RTMP delivers a single one on any
  FFmpeg below 7.1 — which includes Ubuntu 24.04's stock build. Defaulting to
  either silently hands a share of installs the wrong thing, and the RTMP failure
  is invisible: the stream works, and one of the six tracks arrives.
- **The video treatment on a destination is two cards, not a dropdown.** Copying
  video is `-c:v copy` and costs nothing; a shared encode is the most expensive
  thing on the page. A `<select>` presented them as the same kind of choice. The
  picker now leads with what an encode *produces* rather than what someone named
  it, and states the consequence of joining or leaving one — including what
  happens to the encode you are leaving, which nothing else in this space tells
  you.
- **A motion scale that is actually wired.** `--motion-instant/quick/settle` and
  `--ease-out` were declared and registered with Tailwind zero times, so all 27
  `transition-colors` ran on its 150ms default — and the reduced-motion block
  that set those tokens to `0ms` did nothing at all. The website, which had no
  named timings, now shares the same scale.
- **The E-RTMP multitrack harness is Go**, not Python. It was the repository's
  only `.py` file; everything else that stands up a real stream and measures what
  comes back is already a `//go:build ignore` main under `scripts/`.


### Fixed

- Chat's `(i)` help buttons no longer all announce as "More information"; each
  names the setting it explains, so a screen-reader user can tell a dozen of them
  apart.
- **A data race in the RTMP listener.** `serveSubscriber` assigned `sub.conn`
  after publishing the subscriber into the stream table, so `Stop` could read the
  field while it was being written. Found by CI's `-race`; no local run had ever
  used it.
- **Enhanced RTMP multitrack was unusable for any subscriber that joined late.**
  Tracks 2..N arrive wrapped in `AudioExMultitrack`, and the setup cache did not
  unwrap them — so a late subscriber held coded frames for tracks it had no
  decoder configuration for, and `ffprobe` hung rather than failing. Late is the
  normal case: the ingest child subscribes when the source is enabled, and the
  operator starts their encoder whenever they like.
- **Every healthy SRT install reported its ingest as offline.** The header read
  `status.ingest.state`, and SRT deliberately has no ingest child — `srtserver`
  delivers straight into the hub — so the most prominent status in the
  application contradicted the meters, the LIVE badge and the API. It now asks
  the question the rest of the app asks: are bytes arriving.
- **The reconnecting indicator faded itself.** `live` pulsed a separate halo and
  kept its core opaque; `warn` pulsed the core, dropping the only indicator to
  35% opacity twice a second. The state that most needs attention was the one
  rendered hardest to see.
- **Twenty-one raw Tailwind colours collided with the semantic tones**, and the
  guard whitelisted them: `red-500` is ΔE 8.11 from `--down`, which is to say
  indistinguishable.
- **Recordings were truncated on stop rather than finalised**, on every platform
  and not only Windows as first recorded.
- **`--tls acme --yes` spun forever, and a re-install never restarted.**
- **The default Docker install reported unhealthy forever.**
- **An unmatched `/api` path answered 200 with the UI instead of a 404.**
- **The Docker upgrade backup archived an empty volume and exited 0.**
- **A short window drew two scrollbars**, one of which scrolled 14px of nothing.
- **A fresh install logged an ERROR about an RTMP port nobody had set.**
- **The installer's FFmpeg download could be walked down to plaintext HTTP.**
  Every other fetch in `install.sh` pins the scheme; this one went through
  `urllib.request.urlretrieve`, which follows redirects without restricting it —
  and it writes a binary into `/usr/local/bin` that a root service executes.

## [0.3.0] — 2026-08-05

A minor bump rather than a patch, and deliberately: `facebook.backupIngest`
became `backupIngestWanted` with no compatibility alias, which breaks any client
that *writes* that field. Everything else here is a fix.

An adversarial audit of the 0.2.0 release, and the fixes for everything it
found. The full record, with a numbered finding behind every change below, is
[docs/roadmap/ADVERSARIAL-AUDIT-0.2.0.md](docs/roadmap/ADVERSARIAL-AUDIT-0.2.0.md).

### Security

- **A stream key could reach your MQTT broker as a retained message.** FFmpeg
  prints the whole publish URL when a destination refuses it, that line became
  the process's last error, and the last error was published to MQTT as
  `ingestError` — **retained**, so it outlived the process and was readable by
  every subscriber on the broker with no session at all.

  **If you run the MQTT integration, clear your retained topics after
  upgrading.** Upgrading stops new keys being published; it cannot unpublish
  what a broker is already holding.

  The same bytes were also returned unmasked by `GET /processes` and its logs
  endpoint, and written to `process.log` on disk — both behind authentication,
  so no privilege boundary was crossed there, but a log file is the artifact
  people attach to bug reports. All of it is now masked where the line is
  captured. Expert mode still shows the real command deliberately: it exists so
  an operator can approve the command that will actually run.

### Changed

- **BREAKING, for API clients only:** the destination field
  `facebook.backupIngest` has moved to a top-level `backupIngestWanted`. It was
  inside a platform-named block while the engine that reads it is
  platform-neutral, which meant no platform but Facebook could ever have a
  redundant feed.

  Stored rows migrate on first open — **there is no compatibility alias**, so a
  script that *writes* `facebook.backupIngest` stops having any effect. One that
  only reads a destination is unaffected apart from the new name. See
  [docs/API.md](docs/API.md).

- Enabling or disabling backup ingest from the destination dialog now works.
  Unchecking it previously sent nothing, and the server kept the stored `true` —
  so the toggle was one-way and the second feed kept running at double the
  upload.

- A YouTube made-for-kids declaration can now be withdrawn. Setting it back to
  "leave as it is on YouTube" previously sent nothing and the stored value
  survived.

### Fixed

- **A deleted source could leave an FFmpeg publishing forever.** Shutting an
  engine down stopped a destination's primary process and not its backup, and
  the backup restarts itself by design — so it kept publishing to the platform,
  invisible to the process list, holding its relay port, until the daemon
  itself was restarted.
- **A standby SRT encoder and the primary evicted each other.** Both were keyed
  by source alone, so whichever connected first held the slot and the other was
  refused; if the primary went quiet for three seconds the standby took over,
  and the recovering primary was then the one refused. Redundant ingest did not
  work in the situation it exists for.
- Two schedules starting the same Facebook destination no longer move one
  broadcast back and forth between their times every five minutes. Each show
  gets its own event page.
- The pre-announce sweep no longer reverts an operator edit that lands while it
  is talking to Facebook, and no longer changes the stream key of a destination
  that is currently live.
- A backup feed now appears in the process list, so its command line and logs
  are reachable — which is what you need at the moment redundancy breaks.
- A reconnect-policy change now reaches a destination's backup as well as its
  primary, and revives one that had already given up under a stricter limit.
- Editing an expert argument's whitespace, or changing a setting that does not
  alter the command line, no longer drops a live connection.
- A destination nobody has touched is no longer reported as retuned on every
  reconcile.
- The hooks card and the playlist editor are translated. Creating a webhook
  twice by double-clicking, which produced two hooks and showed one signing
  secret, is fixed.

### Internal

- Concurrent reconciles are serialized. Two could previously start the same
  destination, leaving one FFmpeg running that received no packets and that
  nothing could stop.
- Providers gained one injection point for their HTTP base. The previous
  mechanism redirected one call in thirteen while looking like it redirected
  all of them, and two other providers hard-coded past their own.
- Six guards that could not fail were rewritten, and the classes they belonged
  to were swept across the codebase — route tests that assert only a status
  code (the embedded UI answers an unrouted API path, so they pass with the
  route deleted), and source-text assertions that survive their block being
  switched off.

## [0.2.0] — 2026-08-04

### Added

- **Compliance metadata now reaches the platforms.** A YouTube COPPA
  self-declaration, a YouTube privacy status and a Twitch content label were
  editable, validated, saved — and had never once been sent. `PushCompliance`
  existed from the first release and no code path called it, so every value an
  operator set stopped at the database.

  This is a behaviour change on upgrade, and a deliberate one: an install with
  compliance already stored begins writing it to the platform on the next push.
  A destination configured months ago with a privacy setting nobody remembers
  will apply it. Two destinations pointing at one account with *different*
  compliance refuse the push and name both, rather than letting one silently
  win — discarding a legal declaration with nothing anywhere saying so is the
  failure this change exists to end.

- **Facebook accepts the metadata everything else already did.** Title,
  description and tags on the composer push; audience, crossposting and the
  donate button applied when the broadcast is created. A tag word Facebook
  cannot resolve is a warning naming the word, not a failed push.

- **A Facebook show can be announced before it starts.** A destination on a
  start schedule gets its broadcast created ahead of time, so there is a public
  event page days early rather than one appearing the moment bytes arrive.

  A `once` schedule further out than Facebook's seven-day limit still saves and
  still runs — it simply gets no event page, and is told so.

- **A Facebook destination can publish a redundant backup feed.** A second
  supervised FFmpeg to Facebook's backup ingest, so a dropped connection does
  not drop the broadcast. Off by default: it doubles that destination's upload
  bandwidth, which is an operator's call on a thin uplink. Enabling it costs one
  reconnect and the dialog says so — a backup endpoint exists only on a
  broadcast created with one, and creating that issues a new stream key.

- **Every release carries a software bill of materials.** SPDX and CycloneDX,
  scanned from the source tree rather than the binaries, so the embedded web
  UI's npm dependencies appear alongside the Go modules and the pinned Actions.

- **The failover selector can put a playlist on air.** A fourth candidate,
  ranked below both ingests and above the slate: a scheduled programme is a
  fallback for "nobody is streaming", not a pre-emption of somebody who is, so a
  presenter who is live stays live. An operator who wants the playlist to win
  anyway pins it, the same way they would pin the backup.

  What it changes for a viewer: an outage that used to land on the standby card
  now lands on programming, and a selector already sitting on the card leaves it
  for the playlist rather than waiting for an encoder. The return to a real
  ingest is immediate and is not subject to the return mode, exactly as leaving
  the slate already was — there is no flapping risk between a file and an
  encoder to bound.

  Five new reasons reach `Failover.Reason` — `the playlist is running`, `the
  primary ingest stopped delivering and the playlist is running`, `neither
  ingest is delivering, so the playlist is on air`, `an operator selected the
  playlist`, and `the playlist stopped running` when it ends and the standby
  card takes over. None of the existing fourteen were reworded. The decision table
  frozen in `internal/engine/testdata/selector_golden.txt` grows from 1024 rows
  to 3200, and a second table proves the addition is additive: all 1024
  decisions that predate the playlist are byte-for-byte what they were,
  reason strings included. The settings that turn a playlist feed on, and the
  tier that answers them, are the entry below.
- **A file can hold the stream, and failover keeps watching while it does.** The
  playlist is a supervised FFmpeg loop publishing into **a relay hub of its
  own**, and that hub is the whole of the design. A file played into the
  *primary's* hub puts bytes on it; the primary therefore reads live; and
  failover never switches away from a live primary — so a file on air would have
  disabled the entire failover feature, silently, for as long as it played. With
  its own hub the primary's stays empty and every failover decision underneath
  stays reachable. `scripts/acceptance-failover.sh` measures precisely that: the
  encoder drops while the file is on air, the primary is *seen* to go down, and
  the destination rides the switch onto the file without restarting.

  Two settings turn it on — `failover.playlist.enabled` and
  `failover.playlist.filePath`, the path relative to the data directory and
  confined to it exactly as a `file://` pull source and the slate's still are. A
  path that escapes it is refused at validation and never becomes an FFmpeg
  argument. There is no form control for them yet: they are reachable through the
  settings API, and the control belongs with the sequencing work rather than
  with this. Both are hot-reloadable and neither disturbs anything else — a save
  that touches the recorder or a destination costs the playlist nothing, and
  editing the path restarts only the playlist's own process.

  Disabling the playlist while it is on air hands the stream to the slate in the
  same breath, rather than leaving the selector holding a source whose hub has
  just been closed. That was worth two seconds of dead air on every destination
  while the failed-start backoff expired, on an ordinary operator action, and it
  is fixed here rather than shipped: a tier that has been torn down stops
  counting as a candidate at the instant it goes, not at the next sweep.

  **The file's codec parameters must match what your encoder sends.** It is
  copied, not re-encoded, so its codec, resolution, frame rate and pixel format
  reach destinations unchanged, and switching onto a file that differs is a
  mid-stream codec change — which platforms answer by dropping the connection.
  The slate cannot cause this because it is synthesised at the departed ingest's
  probed geometry; the playlist can, and nothing validates it yet. Probing the
  file at save time is its own piece of work.
  [docs/SCHEDULED-BROADCAST.md](docs/SCHEDULED-BROADCAST.md) states the
  constraint where an operator meets it.
- **Lifecycle webhooks.** A signed POST the moment the stream starts or stops,
  or a destination goes up or down — one delivery per transition, in order, with
  an HMAC-SHA256 over the timestamp and body. Deliberately not an alert: an alert
  coalesces, debounces and rate-floors because a person is reading it, and a
  script cannot act on eleven events it was never given.

  It delivers an event that did not previously exist. The alert watcher only
  emits `ingest.recovered` after it has emitted `ingest.lost`, so an install
  whose streamer connects shortly after boot produced neither — there has been
  no "the stream started" event in polyemesis until now.

  Ordering is per endpoint and is bought with head-of-line blocking, bounded at
  33s by default. A 4xx is never retried. No destination URL, stream key,
  publish token or ingest passphrase reaches a payload, enforced centrally and
  guarded by a test that plants a key in three fields. Automation → Webhooks.
  See [docs/HOOKS.md](docs/HOOKS.md).
- **Chat scrollback sent on connect is now an operator setting**
  (`chat.historyMessages`). This is the in-memory ring a browser reads the
  instant it opens the page, before any stored history is queried — a different
  number from the retention bounds beside it, which govern what is kept on disk.
  Applied live by resizing the ring, so it takes effect on the next connection
  rather than the next restart. Bounded 1–50,000 rather than at the millions
  `keepMessages` allows, because the ring is allocated in full up front: the
  ceiling is memory reserved whether or not anyone is talking.
- **Alert delivery attempts are now an operator setting**
  (`alerts.retryAttempts`). How hard a webhook that is not answering gets chased
  before the alert is dropped, 1–10, first try included. Applied to every
  running engine and remembered for engines created later, so a source added
  after the save does not quietly run a different budget from the rest. Only
  retryable failures are affected — a 404 from a deleted webhook is still
  permanent. The backoff curve underneath stays unexposed: it was chosen against
  measured behaviour and nothing argues for moving it.

  These were the last two knobs `docs/roadmap/UNREACHABLE-KNOBS.md` rated worth
  exposing. Building them surfaced a separate defect: `db.ChatSettings` cited a
  test, `TestChatDefaultsMatchTheChatPackage`, that kept the package default and
  the settings default in step. **That test did not exist in any package under
  any name.** Both pairs are now genuinely pinned.

- **Changing a destination's reconnect policy no longer drops it.**
  `minBackoffSeconds`, `maxBackoffSeconds` and `giveUpAfter` govern what the
  supervisor does *after* FFmpeg exits and never reach the command line, yet
  editing one used to tear the destination down and rebuild it — so the only way
  to say "be more patient with this platform" was to drop the connection to it.
  They are now delivered to the running process. A retune shortens a backoff
  already in flight and never lengthens it, and never resets the restart
  counters. Raising the give-up threshold on a destination that has already
  given up does restart it, deliberately: leaving it failed for ever would be
  the silent no-op this work exists to remove.
- **Saving settings now reports what it did.** `PUT /api/v1/settings` returns a
  `reload` array alongside the settings, naming what restarted, what applied
  live, and why. "Saved" is a statement about the database; this is a statement
  about the stream. See [docs/HOT-RELOAD.md](docs/HOT-RELOAD.md).
- **Every settings field now has a recorded reload class.** 141 rules — one per
  leaf of the settings tree — each naming a class, the function that carries the
  change, and a reason. A reflection walk fails the build when a field is added
  without one, when a rule names a field that no longer exists, or when it names
  a function nobody wrote. The distribution: 89 live, 49 respawn, 1 rebind, 2
  on-demand, 0 next-start.

- **Job-history retention now applies without a restart.** `postProd.retainDays`
  and `postProd.retainJobs` were read exactly once, at boot, so lowering them
  did nothing an operator could observe until the next restart — the settings
  page said saved and the history did not move. The purge now runs hourly and
  re-reads its settings each sweep, the same shape recording retention already
  used. Found by the reload classification table, which refused to call it live.
- **The sidebar collapses to an icon-only rail on desktop.** A footer chevron
  or Ctrl/Cmd+B toggles it, and the preference is remembered in
  `localStorage`, the same place `polyemesis.language` already lives. Below
  the `md` breakpoint the off-canvas drawer is unchanged.

### Fixed

- **An IPv4 publisher could not reach the SRT ingest on macOS, and neither end
  said why.** A bare `:6000` became one dual-stack socket, and the reply to a
  v4-mapped peer went out as an IPv4 control message on an `AF_INET6` socket —
  which Darwin refuses and the dependency discards. The datagrams arrived, the
  handshake never completed, and the typed refusals that explain every other
  rejection never ran.

  A wildcard address now binds `0.0.0.0` and `::` as two listeners, neither of
  which takes that path. If one family cannot be bound, the other still serves.
  Fixes issue #28; the upstream report is `datarhei/gosrt#148`.

- **A metadata push could report a result before it had one.** The composer is
  promised a 202 with every account pending, and the workers were started
  *before* the job was snapshotted — so a platform that failed instantly could
  appear in the reply that was meant to say "not yet". With no developer
  credentials configured, that takes microseconds.

- **Settings a destination's platform cannot send are now cleared, and said.**
  Configuring a destination for one platform and then switching it left the
  first platform's settings behind, invisible — the compliance panel renders
  only for the platforms that have one. Inert while it stayed there, and live
  again if the destination was ever pointed back.

- **A scheduled Facebook broadcast that no longer exists is replaced.** If the
  operator deletes the video on Facebook, the reschedule refuses forever. Three
  consecutive refusals now mean the broadcast is gone rather than briefly
  unreachable, and a fresh one is created. One transient failure changes
  nothing.

- **`meters.intervalMs` was stored, reported as saved, and ignored.** The value
  was captured into the metering sidecar's stdout handler when it spawned, and
  it is not part of that process's restart signature — so changing it did
  nothing at all until some unrelated edit happened to restart the meters.
  Lowering the interval to watch a quiet channel appeared to work and did not.
  The throttle now reads the current value per frame.
- **The VA-API probe tested the wrong GPU on a multi-GPU host.** Detection
  already ranked the render nodes under `/dev/dri` and handed the encode the
  right one; the probe ignored that and named `/dev/dri/renderD128` regardless.
  On a machine whose first render node is display-only — or a container passed
  `renderD129` — VA-API was tested on hardware it would never run on, failed,
  and was withheld from the editor on the strength of it. The probe now names
  the node detection chose. Where detection finds no usable node it still names
  `renderD128`, because a VA-API probe with no device fails on every machine
  including the ones where VA-API works, and "no node found" must not be
  reported as "this encoder is broken".

### Changed

- **A hung acceptance suite now says what it was waiting for.** Every suite
  carries a deadline below the CI job's own, so a stall reports the last step it
  reached, how long it had been there, the live processes and the tail of the
  server log — rather than being cancelled by the job ceiling with the log
  ending mid-sentence. The install steps are bounded too, after one job spent
  its whole budget inside `apt` and looked exactly like a suite hang.

- **A timed-out wait in the acceptance suites now reports what it observed.**
  `acceptance-failover` and `acceptance-mqtt` both asserted causes they had
  never measured, and `acceptance-mqtt` discarded `docker run`'s stdout, stderr
  and exit status at the call site. Failures now carry the trajectory of every
  sample taken, whether the value arrived just after the ceiling, and — for the
  broker — the container's exit code, status and last log lines. No timeout was
  changed and no retry was added; the flake rate is still the thing being
  measured. See issue #38.
- **The Windows service now announces the recording-truncation defect itself.**
  Stopping the service still truncates the in-progress recording — the graceful
  stop is a `CTRL_BREAK` console event and a service has no console — but the
  service no longer lets that happen silently. Every start writes a warning to
  the Event Viewer under event ID 3, naming the cause, the bound (only the
  segment in progress; earlier segments are finalised and playable), and the
  workaround. `docs/TROUBLESHOOTING.md` carries the same under the symptom.
  The defect itself is unchanged and remains open: both candidate fixes —
  allocating a console, or asking FFmpeg to quit over stdin — need a real
  Windows host to verify, and shipping an unverified fix to the process
  machinery every stream depends on is the worse trade.

## [0.1.0] — 2026-07-31

The first tagged release. Entries are grouped by what they do for an operator
rather than by which package changed.

**What it is for:** one encoder in, many platforms out, and **a different audio
mix for each destination**. Video is never re-encoded on a destination path, so
a dozen destinations cost roughly what one does.

**Read [Known limitations](#known-limitations-at-010) before deploying it.**

### Multi-source ingest

- **Several independent programmes in one instance.** Each source has its own
  ingest, its own destinations, its own renditions and its own recordings. The
  motivating case is a horizontal and a vertical programme from one OBS with the
  vertical plugin, going to different platforms.
- **One-port ingest, addressed by token.** All sources can share a single SRT
  port, with the per-source token acting as the address. This improves on the
  approach it was inspired by in two ways: the token *is* the streamid rather
  than being a server-wide secret alongside a name, and a stale connection is
  taken over after 3 seconds instead of locking out the encoder that is trying
  to reconnect.
- **Token rotation with a grace period.** A rotated token keeps working for five
  minutes, so rotating one does not cut off a live encoder.
- **Typed SRT rejections.** A refused publisher is told *why* — bad passphrase,
  no such token, already publishing — rather than getting a generic close.

### Failover and synthetic sources

- **Source selector.** A permanent relay between the ingest and everything
  downstream, so a source switch never restarts a destination and never drops a
  platform connection. Off by default: with it off the pipeline is byte-for-byte
  what it was without the feature.
- **Standby slate.** A generated picture, built at the *probed* geometry of the
  departed ingest so a copying destination does not choke on the change.
- **Silence tier.** A video-only ingest — which every major platform refuses —
  gets a synthesised silent stereo track, so destinations keep working instead of
  crash-looping on a track that is not there.
- **Automatic or manual return** to the primary, with a configurable stability
  window.

### Audio

- Per-destination multichannel routing: each destination selects and mixes its
  own tracks from one ingest.
- Track annotations describing what each incoming track *is* — mic, music,
  commentary, language — held against the source rather than the destination.
- EBU R128 loudness measured **after** routing, which is what the platform on the
  other end actually receives.
- Per-destination loudness targets, ducking, and audio delay.

### Post-production

- Recording, segmenting, and a retention policy with a free-space guard.
- Job queue with a governor, pausing, retries and a policy UI.
- Whisper transcription with FTS5 search across transcripts.
- Media proxies, keyframe indexes, and a clipper.
- Rolling clip buffer for capturing a moment that has already happened.

### Platforms

- Providers for the major platforms, with OAuth where it is offered.
- Unified chat across platforms.
- Platform capability presets, so a destination is configured with limits the
  platform will actually accept.
- **Metadata push** — title, description, category and tags, shaped per platform
  and reporting what each one refused rather than claiming success.
- **Kick's stream key is fetched, not pasted.** It was recorded here as
  unavailable for a long time: Kick publishes no `/streamkey` endpoint, so
  reading the endpoint list finds nothing. The key rides as `stream.key` on the
  channels response polyemesis already fetches, withheld unless the token
  carries `streamkey:read`.

### Chat moderation

Delete, ban, timeout and lift, across all four connected platforms. Before this
only one of the four could delete.

- A **cross-platform moderator user card**: what one person has said, newest
  last. No platform here publishes an API for a user's message history —
  Twitch's own mod card is a web-app feature backed by internal endpoints — so
  this reads polyemesis's retained scrollback. Shallower than Twitch's card, and
  it works across four platforms at once.
- Facebook's reversible **hide**, and a local-only hide everywhere else.
- Twitch **channel rules**: slow mode, followers-only, subscribers-only,
  no repeated messages.
- **Chat retention is configurable** — hours and message count — where it was
  previously hard-coded at two hours and 2000 messages.

Two platform traps are handled rather than inherited:

- `DELETE /helix/moderation/chat` with **no** `message_id` deletes every message
  in the channel and returns success. An empty id is refused before the URL is
  built.
- Kick counts timeouts in **minutes** where YouTube and Twitch count seconds, so
  a unified `600` would mean ten minutes on two platforms and seven days on the
  third. Each adapter converts at the last moment and rounds **up** — truncating
  30s to zero minutes would reach Kick as a permanent ban.

### Overlays

- **Image watermarks** on renditions: nine anchors, size and margins as
  percentages of the frame so the same logo is correct on landscape and vertical
  tiers alike.
- **Text overlays**: content, font, anchor, size, colour, margins and an optional
  background box. Two weights of Inter ship embedded, because FFmpeg's
  `drawtext` takes a font *path* and a container image has neither fontconfig nor
  a font file. A build without `drawtext` drops the text and keeps the picture up
  rather than failing the rendition.

### Per-destination tuning

For the destination that needs one setting nothing else does:

- **Transport and muxer**: mux queue limits in packets or bytes, read/write
  timeout, and the flags that stop FFmpeg guessing a duration it cannot know.
- **Resilience**: minimum and maximum reconnect backoff, and a give-up
  threshold counted on **consecutive** failures — so a destination that
  reconnects once an hour for a week never accumulates its way to the limit.
- **Audio**: bitrate, codec and mono output per destination.
- **Expert arguments**, spliced into the FFmpeg command line, with a dry run
  that tells you whether the result would start without starting it. Treat
  access to it as equivalent to shell access.

### Media uploads

- **Put a file on the server from the browser** — Library → Media. Before this,
  broadcasting from a file meant copying it onto the box yourself: fine on a
  Linux host you already have a session on, a wall for everyone running the
  container.
- **The filename you send is discarded.** This is the only endpoint where a
  caller supplies both the bytes and something path-shaped, so the server names
  the file itself, with a random suffix that also stops an upload overwriting
  an existing one by guessing its name.
- **Nothing is left behind by a failure.** The body streams to a temporary file
  and is renamed on success, so an oversized, empty or cancelled upload leaves
  no partial file — which matters because a half-written video is not visibly
  broken in a listing, and the operator would find out when the broadcast they
  scheduled went to air.
- **Free space is checked before the write**, not discovered during it. A
  filled volume takes the database and the HLS preview with it.
- **Every file carries an origin** — *uploaded*, *recorded* or *clip* — derived
  from which store it came out of rather than stored beside it. Uploads live in
  their own directory, so a retention policy written about footage the server
  captured cannot delete a file an operator deliberately put there.

### Automatic chat moderation

Three checkers, cheapest first, and a switch matrix over all of them.

- **Rules** — regular expressions over one message. Go's `regexp` is RE2, so an
  operator-supplied pattern cannot become a denial of service. Normalisation
  defeats the evasions people actually use: case, padded and doubled letters,
  zero-width characters, leetspeak and Cyrillic homoglyphs.
- **History** — a *sequence* from one author, and the layer neither of the
  others can replace. Rate and repetition are properties of a sequence, so no
  per-message classifier can see them: ten identical messages are individually
  innocuous and collectively the commonest abuse there is. Detects flooding,
  repeated phrases through case and spacing, link and mention spam, and
  sustained upper case.
- **Model** — an optional external API, asked only about what the first two
  could not settle, and **off by default**. Any OpenAI-compatible endpoint,
  including a locally hosted one.

**The switch is three-dimensional: action × platform × checker.** The same
action deserves different trust depending on the evidence — a regex hit is
reproducible and a model verdict is not — and an operator's exposure differs per
channel. Cells are gated by what each platform can actually do, and an
unavailable one renders inert *with its reason* rather than as an unticked box:
a switch that silently does nothing leaves the operator believing that channel
is protected.

**Nothing is automatic on a fresh install except flagging for review.**
Auto-ban is offered wherever the platform supports it and defaults off for every
checker, the model included — refusing to expose a capability is not a safety
feature, it is a decision taken away from the person who knows their channel.
There are per-platform and global kill switches, because mid-incident nobody
should be unticking fifteen boxes.

**It fails open, everywhere.** A model timeout, 500, rate-limit or unreadable
key lets the message through and flags it; a rule that will not compile is
logged loudly and the other checkers keep running; the action queue drops rather
than blocking, because blocking would stall chat for every viewer during exactly
the raid it exists for. A moderation outage must not silence a chat.

Messages display *before* automod sees them and are retracted after, so nothing
here can make chat feel slow.

### Operations

- Prometheus metrics, alert rules with webhook delivery, and schedules.
- **MQTT telemetry**, retained, with Home Assistant discovery — so the stream
  appears as entities in a dashboard the operator already runs.
- TLS with automatic certificate selection; HSTS deliberately opt-in and never
  sent over a self-signed certificate.
- API tokens, hashed at rest and individually revocable.
- **Sessions are revocable.** A stateless JWT cannot be deleted server-side, so
  changing the password bumps a token epoch and every token issued before it
  stops working — which is what "somebody else has my session" has to mean.
- Fourteen UI languages.
- **An interactive Linux installer** (`scripts/install.sh`), in Docker or binary
  mode, with rollback on failure. It exists for the traps that a hand-written
  `docker run` reliably falls into: `/udp` on the SRT port, a 30-second stop
  grace period so a recording is finalised rather than truncated, a UDP firewall
  rule, and `CAP_NET_BIND_SERVICE` when ACME is chosen. Binary mode refuses to
  proceed below the FFmpeg 6.0 floor — naming the caller's distribution version
  where it recognises it — and verifies the download against the release's
  published `SHA256SUMS`. It never handles a password, because there is no
  account until the first-run screen creates one.

### Security

Findings from a pre-release review, each fixed with a test that fails when the
fix is removed:

- **The Kick webhook now verifies its signature.** The hook existed, was
  nil-checked, and was never assigned at its one construction site — so
  verification silently never ran and the unguessable callback path was the only
  control. A missing verifier now refuses the delivery instead of skipping the
  check.
- First-run setup is atomic, so two concurrent requests to an uninitialised
  install cannot both create an administrator.
- The webhook path secret is compared in constant time, like the playout and SRT
  tokens already were.
- Every database read is a compile-time constant, so no value can reach the SQL
  text by construction.
- CI gained dependency, secret and static-analysis scanning — gated on proof
  that the scan actually examined the tree, after a run reported zero findings
  having read zero files.

### Fixed

Bugs worth naming, because each was found by measurement rather than by review:

- **A file destination could never restart.** FFmpeg refuses an existing output
  path, so the first respawn died with "already exists" and every one after it
  did the same — the destination crash-looped and the operator was left with a
  zero-byte recording. The output path is now chosen per spawn, and never
  overwrites existing footage.
- **`amix` was quietly dropping a three-track mix by about 9.5 dB**, because its
  default divides by the input count. Now `normalize=0`.
- **A negative audio delay was doing nothing.** `-itsoffset` shifts every stream
  of an input together, so audio and video moved in lockstep and the delivered
  offset measured 0 ms for every requested value. Replaced with the `setts`
  bitstream filter, which moves video alone and preserves `-c:v copy`.
- **A data race in the event broker** lost dropped-message counts under load.
- **Editing the ingest in Settings did nothing** once sources arrived, because
  the engine read the source row instead. Settings now writes through.
- **The Sources page sent server-computed fields back in its PUT**, so every save
  returned 400 and the control silently reverted.
- **A live-stream restart on every blur.** Editing a port committed on focus
  loss, so tabbing out of a field was an outage. Changes are now explicit.
- **The relay forwarded zero-length datagrams**, which FFmpeg reports as EOF —
  one empty datagram would have ended every consumer on a hub at once.
- **The VA-API image could not be built at all.** An automated
  `bump ubuntu from 24.04 to 26.04` changed `Dockerfile.vaapi`'s `FROM` line and
  left the FFmpeg pin at a 24.04 archive revision that does not exist on 26.04.
  Nothing caught it: the release workflow runs only on a tag, and the container
  suites run only on `main`. Found by rehearsing the release. The image now
  pins Ubuntu 26.04's FFmpeg **8.0.1**, up from 6.1.1, which also brings AV1
  VA-API and QSV encoders.

### Testing

- Acceptance suites that **measure** rather than assert: audio routing proved by
  bandpass RMS per track, video passthrough proved by frame-level `framemd5`
  identity, failover proved by counting destination restarts and backwards
  decode timestamps across a real source switch.
- Browser end-to-end suite against the shipped container.
- Fixed-value guards on suite check counts, after one suite reported
  *"7 passed, 0 failed — PASSED"* having silently skipped five checks.
- **A real broadcast now runs on Linux, macOS and Windows on every push**, not
  just a health check. The cross-platform job pushes a three-track stream in,
  compiles two destinations with different track selections, and measures
  per-band energy in each output — so a destination silently carrying the wrong
  mix fails, where a check that only asked whether FFmpeg exited 0 would pass.
  This is the part built on process groups, signals and path construction, which
  is exactly where Windows differs.

### Documentation

An accuracy pass over every page, checking each claim against the code rather
than reading for tone. What it found is why it was worth doing:

- The quickstart's **first command pulled from a registry the project has never
  published to**, and its OBS instructions contradicted `OBS.md` in a way that
  produced a working stream carrying a single audio track — configuring away the
  only reason to run polyemesis.
- `SECURITY.md` and `TLS.md` both promised that **every** response carries
  `X-Frame-Options: DENY`. The embeddable player deliberately drops it. A
  security policy that overstates its own coverage is worse than one that states
  the exception.
- `TLS.md` documented the `:80` redirect as `301`/`308`, which holds only when
  `tls.hostname` is set; without it the target comes from the client's own `Host`
  header and the redirect is deliberately temporary and uncacheable.
- `HARDWARE.md` contradicted itself on render-node selection. Detection does
  choose a node and the encode honours it — but the *probe* still names
  `renderD128` unconditionally, so on a multi-GPU host it tests the wrong device
  and the editor declines to offer VA-API on the strength of it.
- `INSTALL.md` claimed `make release` emits no Windows target. It emits two.
- The stale "nobody has run this on Windows" claim appeared on four separate
  pages. Platform status is now stated on two axes instead of one adjective —
  what CI proves, which is identical for Linux, macOS and Windows, and what
  operating it proves, which is not — so a reader can see where the platforms
  are equal rather than inferring it from a word.
- Every file link and all 110 anchor links across 51 markdown files were
  machine-checked and resolve.

### Known limitations at 0.1.0

Stated here rather than discovered later. None is a bug; each is a boundary.

#### Access and identity

- **One user, no roles.** Access to the UI is full control of the server's
  streaming and — through expert mode and file destinations — meaningful control
  of the machine. Put a reverse proxy in front if you need access control.
- **No audit log.** Nothing records which change was made when, or from where.
- **OAuth needs your own developer app.** Signing in to YouTube, Twitch, Kick or
  Facebook works, but each operator registers their own application per platform;
  there is no shared one. Stream URL and key need none of this. See
  [PLATFORMS.md](docs/PLATFORMS.md).

#### Ingest

- **RTMP serves at most one source**, carries a single stereo pair, and is
  unencrypted. SRT is the path that makes this product worth running.
- **Enhanced RTMP / multitrack FLV is not implemented.** The `enhancedRtmp`
  config key parses and nothing branches on it.

#### Operating systems

- **Windows is tested, not operated.** It clears the same CI floor as Linux and
  macOS including a measured broadcast, but nobody has run a real show on it,
  and **stopping the service truncates an in-progress recording** — the graceful
  stop is a `CTRL_BREAK_EVENT`, which Windows delivers only through a console.
- **linux/arm64 is built and verified at the ELF level, not run.**
- **Homebrew's FFmpeg on macOS has no libsrt.** Use Docker or a build with
  `--enable-libsrt`.

#### Video

- **No hardware encode has ever been run on real GPU hardware.** Detection,
  refusal and fallback are all tested with shims; a successful NVENC, QSV or
  VA-API encode has not been observed. See [HARDWARE.md](docs/HARDWARE.md).
- **The VA-API probe names `/dev/dri/renderD128` unconditionally**, so on a
  multi-GPU host it can test the wrong device and decline to offer VA-API on
  the strength of it. Detection picks the right node; the probe was never wired
  to it.
- **FFmpeg 6.x cuts a copied clip up to one GOP long.** Use 8.x for exact
  out-points.
- No HDR tone-map path, no compositing or video grid, no Decklink/SDI.

#### Playout

- **Scheduled file broadcast joins mid-file, not at frame 0**, and there is no
  playlist sequencing. Files can now be uploaded from the browser, but they play
  one at a time. See [SCHEDULED-BROADCAST.md](docs/SCHEDULED-BROADCAST.md).
- **LL-HLS is not implemented and was declined deliberately** — FFmpeg cannot
  emit partial segments at all. Preview latency was tuned to 2.2–3.2 s instead.

#### Automatic moderation

- **The model checker sends chat messages to whatever endpoint you configure.**
  Off by default, and a locally hosted OpenAI-compatible model keeps everything
  on your hardware. The rules and history checkers send nothing anywhere. The
  endpoint is **not** filtered against private addresses, deliberately: a
  private address is the recommended deployment, and only an admin — who can
  already read your tokens — can set it. See [SECURITY.md](SECURITY.md).
- **No moderation is perfect and this one is not tuned on your community.**
  Every irreversible action starts off for that reason.
- **Automod has not been run against a real raid.** The bounds are tested — the
  author ring holds 5,000 distinct authors, evicts the idle ones, and is capped
  at 20,000 regardless of idleness — but a live incident on a busy channel has
  not happened.

#### Streaming platforms

- **Instagram Live cannot work** and is marked unsupported rather than shipped
  as a preset that never connects.

[Unreleased]: https://github.com/rainmanjam/polyemesis/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/rainmanjam/polyemesis/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/rainmanjam/polyemesis/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/rainmanjam/polyemesis/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/rainmanjam/polyemesis/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/rainmanjam/polyemesis/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/rainmanjam/polyemesis/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rainmanjam/polyemesis/releases/tag/v0.1.0
