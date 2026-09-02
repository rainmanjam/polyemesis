# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

### Added

- **The meters page says when tracks are not being metered, and which programme
  it is showing.** One metering process merges every track, and amerge refuses
  past 64 channels, so a very wide ingest was measured as a prefix while the
  remaining tracks drew flat bars — indistinguishable from silence, on the one
  page whose whole job is telling those apart. The server had counted the
  dropped tracks since the limit existed and the number reached nothing:
  `ffmpeg.MetersDropped` says in as many words that it exists "so a wide ingest
  degrades visibly", and then no caller carried it out of the package. It now
  rides on the source payload and renders as a warning that says *unmeasured,
  not silent*. Absent when nothing is dropped, which is every install anyone is
  likely to run.

  The page also names its programme when the install has more than one. The
  console follows a single programme, resolved to whichever source is first when
  nothing is remembered, and there is still no switcher — so this does not fix
  multi-source metering, it stops the page reporting a slice of the install as
  though it were all of it. See #638 for the switcher.

### Removed

- **The compact/comfortable layout density toggle**, which sat in the console
  header between the username and the language switcher. It rescaled Tailwind's
  `--spacing` inside `<main>` to pack more onto the screen, and remembered the
  choice per browser.

  Removed whole rather than hidden. The preference was applied on mount from
  `localStorage`, so deleting only the button would have left anyone already
  switched to compact stuck there with nothing to switch back — a setting with
  no control is worse than no setting. Gone with it: the `useDensity` hook, the
  `DENSITY` block in `index.css`, and the `dense-grid` opt-in that gave the
  dashboard a fourth column above 1280px.

  `Button`'s `min-h`/`min-w` floors and its `rem`-spelled icon size stay. They
  were written to stop compact from shrinking a control carrying Start and Stop
  below a reliable target, and at a single density they are no-ops — but a
  minimum target size is worth asserting on its own terms rather than left to
  whatever the spacing scale produces.

### Fixed

- **A `--config` path that did not exist booted a different, empty install.**
  `config.Load` returned defaults on a missing file, which is right for the
  implicit `config.yaml` and wrong for a path the operator typed: a typo
  created `./data`, minted a **new `secret.key`**, opened an empty database,
  bound `:8080` in the clear and reopened unauthenticated `POST /setup` — the
  window `-reset-admin` exists to close — while looking healthy. An explicit
  `--config` that is absent now refuses to start, naming the path; the
  implicit default still defaults. (#644)
- **Login throttling could be bypassed behind the reverse proxy the docs tell
  you to deploy.** `deploy/nginx.conf.example` shipped
  `$proxy_add_x_forwarded_for`, which appends to whatever the client sent, and
  the server read the *leftmost* `X-Forwarded-For` hop — the client's own bytes.
  Rotating that header minted a fresh throttle key per request, so the
  5-attempt policy never fired: unlimited online guessing at the single admin
  password, with attacker-chosen addresses in the audit log. The server now
  reads the **rightmost** hop, the one the proxy appended, which is correct
  whether a proxy appends or overwrites; the shipped nginx example overwrites.
  A chain of several trusted proxies now keys everyone behind the last one
  together, which throttles too much rather than too little. (#647)
- **The documentation-drift guards never ran on the pull requests that drift
  the documentation.** The guards are Go tests — `platforms_doc_drift_test.go`
  reads `docs/PLATFORMS.md`, `api_docs_route_table_test.go` reads
  `docs/API.md` — and every Go step in CI is gated on the change touching
  code, so a documentation-only pull request ran none of them and reported
  green as "did no work". That is how `docs/UPGRADING.md` came to tell an
  operator, mid-migration, that a CI-tested feature does not work, and how
  `docs/MODULES.md` came to name a base image and a Go version no Dockerfile
  uses. The docs-only path now discovers the packages whose tests read
  `docs/*.md` and runs them; it costs seconds, because those tests read
  markdown and need no ffmpeg, database or network. It also counts what ran
  and fails on a low count, since `go test -run` matching nothing exits 0.
  (#651)
- **`update.sh` backed up a live database and never checked the copy opened.**
  Both generated scripts copied the data directory while the server was still
  running — `cp -a` for binary installs, `docker run … tar czf` for compose —
  and stopped the service afterwards. The guard checked that `polyemesis.db`
  and `secret.key` *existed*, then printed "backup verified". Migrations run
  forward only, so that copy is the only way back from an upgrade, and every
  way it can exist without being usable leaves a file of plausible size. Both
  scripts now stop the service **before** copying, and the copy is opened,
  walked with `integrity_check` and checked for this server's schema by the
  installed binary itself (`polyemesis -verify-backup`), which runs no
  migration. The binary install also keeps the running executable as
  `polyemesis.previous`, since the rollback instructions said "reinstall the
  previous binary" without keeping one. (#643)
- **The console printed credentials as readable text in five places.** The
  Sources page showed the publish token twice — once as `STREAMKEY`, once as
  `TOKEN` — in plain text on the page an operator opens while someone is
  helping them get a broadcast up. 0.8.0 masked the stream key *input*; it did
  not touch these, because they are not inputs. Also fixed: the webhook signing
  secret shown after creating a hook, the API token shown after minting one,
  and the ingest URL on the dashboard and in Settings, which embeds the SRT
  passphrase in cleartext for an admin (the server masks it only for a
  read-scope principal).

  All now render through a new `SecretCode`: masked to a fixed width, with a
  deliberate reveal. **Copy still works while masked** — moving a secret to the
  clipboard never required putting it on the screen. The webhook secret gained
  a Copy button it never had, so reading it off the screen is no longer the
  only way to get it.

  Not masked, deliberately: the RTMP address beside the stream key, and an
  ingest URL with no credential in it. Masking those would train an operator to
  press reveal on everything, which is how a mask stops meaning anything.

  `secret-fields.test.ts` grew the check that would have caught this. It asked
  whether any `<Input>` was bound to a credential — a question no `<code>` block
  can fail. It now also asks whether one is printed into element text, and it
  strips comments first, because the first version failed on the docstring
  explaining the bug.

## [0.8.0] — 2026-09-01

### Changed

- **`uptimeSec` now counts from the first media, not from the spawn.** An ingest
  is started *listening*, so the old figure included however long it sat waiting
  for an encoder to connect: arming a source in the morning and going live at
  noon reported four hours of uptime for a stream that had been on air for none.
  A process that is running but has seen no media reports `uptimeSec: 0`, which
  is what it is doing. `startedAt` is unchanged and still reports the spawn.
  **This changes the meaning of a published MQTT field** — see `docs/MQTT.md`.
- **Processes with nothing to flush now get a 1-second shutdown grace instead of
  8.** `meters`, `loudness` and `silence` write nothing a reader will ever look
  at — `-f null` for the first two, an mpegts stream to a UDP relay for the
  third — so waiting eight seconds to kill them cost that long on every ingest
  switch and stop, and the waits stack serially. Every other kind keeps the full
  window: an unlisted kind defaults to 8 seconds, so forgetting to classify a new
  process costs latency and never a truncated recording.

### Added

- **A warning when the ingest's video codec is copied to an RTMP destination that
  may refuse it.** Selecting HEVC or AV1 in OBS produces Enhanced RTMP, which
  polyemesis ingests; video is then stream-copied, so that bitstream reaches the
  platform verbatim. FFmpeg muxes HEVC into FLV happily and the platform drops the
  stream — a failure that looks correct everywhere the operator can see. The
  destination card now says so. It suggests and never refuses: a custom endpoint
  that does accept HEVC is a real setup.
- **A bandwidth calculator on the website**, at `/calculator`. It models a
  stream-copying restreamer rather than multistreaming in general: because video
  is copied and not re-encoded, the video half of the sum is one bitstream
  repeated, and it is bounded by the lowest ceiling among the destinations
  receiving it — so sending 6000 to Twitch and 12000 to YouTube is not a choice
  the operator has without a rendition. It reports the encoder's upstream and the
  server's uplink separately, because those are different connections, and
  converts both into data per stream, week and month, which is what a host bills
  on. Platform ceilings are the server's own figures from `internal/db/platforms.go`,
  dated on the page. Custom RTMP and SRT endpoints can be added by name. A
  verbose mode adds overhead, headroom and audio-track inputs, and gives each
  selected destination its own video mode, rendition bitrate and audio bitrate
  in that destination's own row — the case where one platform's ceiling forces
  a rendition and the others keep the copy.
- **Structured data binding the name, the site and the repository.** "polyemesis"
  is a coined word, which helps ranking and hurts identity: nothing told a search
  or answer engine that the name, this site and the GitHub repository were one
  thing rather than three strings that happen to co-occur. The site now emits a
  `SoftwareApplication` node with `sameAs` pointing at the repository, and a
  `WebSite` node for the site as an entity distinct from the software it
  documents. Both assert only what is already true and independently checkable.
  Deliberately absent, and recorded in the layout as a rule rather than an
  omission: no `FAQPage`, no `ItemList` over the comparison pages, and no
  `dateModified` — structured data detaches a figure from the "checked" date
  beside it on the page, and a competitor's price reproduced in a result card
  without that stamp is a stale claim we published.

- **A teardown counter with a denominator.** The supervisor now counts clean and
  killed teardowns per process kind. Previously only kills were recorded, in a
  log line, and a clean teardown wrote nothing at all — `supervise()` returns on
  context cancellation before reaching the exit log — so there was no ratio to be
  alarmed by and no way to see an exceptional path becoming the normal one.

### Fixed

- **The RTMP ingest stream key was rendered in plain text.** Settings drew it
  with an ordinary text input, so the credential OBS authenticates with was
  readable on screen with nothing marking it as a secret — during exactly the
  activity that puts a screen in front of an audience.
- **Secret fields had no way to reveal what was typed.** The SRT passphrases,
  the MQTT password, the OAuth client secret and the destination stream key were
  masked but had no toggle, so checking a pasted key meant saving and reopening.
  All six now use the existing `SecretInput`, which masks by default and reveals
  only on an explicit press. A test now fails if any input bound to a credential
  is not one, because the component already existed and nothing required it.
- The reveal control is now translated rather than hardcoded English.

## [0.7.0] — 2026-08-28

### Upgrading
- **Building from source now needs Go 1.27.** `go.mod` declares `go 1.27.0`, so
  a 1.26.x toolchain refuses the build outright rather than degrading — a hard
  stop with no release note was the only thing wrong with it. Nothing changes
  for anyone installing a binary or running the container; both ship their own
  toolchain.
- **TLS now serves on :443 rather than :8080 when a certificate is configured.**
  An install reached at `https://host:8080` moves, and a firewall that only
  opens 8080 makes the console unreachable after the upgrade with nothing on
  screen to say why. Open 443, or set the listen address back explicitly.

### Fixed
- **Stopping one destination had no confirmation, and the button could invert
  under the cursor.** It ends a live broadcast that cannot be resumed, and it
  had less protection than either neighbour: bulk stop-all requires a
  server-side confirmation and delete requires typing the name. Stop is now
  held briefly with a visible Undo — a confirmation dialog in front of a
  frequent deliberate action is trained away within a day, after which it costs
  a click and protects nothing. Separately, the button swaps between Start and
  Stop in place on a status repaint that arrives every two seconds, so a click
  aimed at Start could land on Stop; a click is refused for a moment after the
  action changes, and says why. (#506)
- **Clips and the loudness monitor were broken on any multi-programme install.**
  Four routes scoped inside the handler rather than at the router, which a
  survey of the router's middleware could not see. The meters page's "NOT
  UPDATING" was a staleness counter reporting refused reads, not an analyser
  that would not start. (#606)
- **A model spend ceiling was refilled every time settings were saved.** The
  hourly counter lived on an object that `ApplyAutomod` rebuilds on every
  settings save and on the API-key write, so the only bound on model spend was
  reset by the reflex of tweaking a setting mid-incident — which is when
  somebody is least likely to be watching the bill. `CallsThisHour` reset with
  it, so the evidence went at the same moment as the limit. The budget now
  outlives the configuration. (#502)
- **The automod confidence floor was copied without a guard.** A stored `0` —
  which is also what an unset field arrives as — removed the floor so every
  model opinion acted; a stored `80`, from reading the 0..1 scale as a
  percentage, sat above every verdict the model can return and silently retired
  the checker. Neither said which. Values outside the usable range are now
  refused at save time and cannot reach the checker. (#503)
- **One bad automod regex silently disarmed every pattern rule.** (#608)
- **Loudness readings outlived the audio they measured.** (#609)
- **A stream key on a non-RTMP destination was stored, never sent, and
  unreachable.** (#610)
- **A destination could publish while nothing downstream of it was running.**
  The path taken when the ingest probe lands started the destinations and
  reconciled none of the things that consume them — the meters, the recorder,
  the preview, the clip buffer, the captions and the loudness analyser all
  stayed on the layout from before the probe. (#612)
- **Per-programme lanes left a large empty column.** Lanes carry their own
  preview, so the page correctly stops drawing the one at the top — but nothing
  replaced it, leaving roughly four hundred pixels of empty page directly under
  the card an operator looks at first. The side cards now sit beside the ingest
  card instead of stacked, which removes the gap and moves the destination list
  up the page. (#614)
- **Five figures on the destination card were never translatable.** Bitrate,
  Uptime, Restarts, Dropped and Speed were hardcoded English. They now have
  keys, translated in all fourteen locales. (#615)

- **A scheduled broadcast could silently fail to go on air on a multi-programme
  install.** `schedules` carries no `source_id` — a timetable is a property of
  the box — but every engine ran its own `scheduler.Runner` over that one table.
  Whichever swept first wrote `enabled` on every destination, including other
  programmes', then reconciled ONLY ITS OWN engine and marked the occurrence
  handled; the other engines read it as handled and never reconciled. Those
  destinations sat enabled in the database with no process publishing, while the
  log said `schedule fired`. `MarkScheduleRun`'s `WHERE last_run_at < ?` is a
  ratchet on the row, not a lease over the work, and the actuator has no way to
  learn whether it won it. There is now one runner, owned by the manager, whose
  reconcile covers every engine — so the shape that caused this is no longer
  representable rather than merely unlikely. The runs page also stops reporting
  the default programme's scheduler as though it were the only one.
- **The dashboard's grouped destination list and the Prometheus scrape lost
  every programme but one.** Scoping `Engine.Status` to its own source was
  right, and it removed a leak three callers were quietly relying on: the
  status payload, the WebSocket push of the same payload, and `/metrics` all
  read the DEFAULT engine, whose status had happened to carry every row on the
  machine. A multi-source install then showed the selected programme's
  destinations and zero for the others, and stopped emitting series for them.
  The metrics half is the dangerous one — a missing series is indistinguishable
  from a destination nobody configured, so an alert on a dead destination never
  evaluates. The destination list now comes from `Manager.DestinationStatuses`,
  every programme's, each still compiled by the engine that owns it.
- **The RTMP relay stopped answering publish and play once gortmplib reached
  v1.0.1.** That release split `ServerConn.Accept` into `AcceptConn` (the
  connection, and reading the play/publish command) and `AcceptAction` (the
  response that admits it). `Accept` still exists, still compiles, and is now an
  alias for `AcceptConn` alone — so the connection came up and no peer was ever
  answered: subscribers reported `Input/output error`, publishers a refused
  connect, against a healthy relay. Nothing in the type system marked the
  change; the end-to-end tests that run real FFmpeg against a real listener are
  what caught it. The bump is taken together with the migration, because
  merging it alone ships broken RTMP.

<!-- RB-5 (#499): this heading was dated 2026-08-21 in #487, while preparing
the tag -- and the tag was then deliberately held. Nothing noticed, so this
section described a shipped release for four days while every install path
(git, GitHub Releases, Docker Hub) still ended at v0.6.0, lacking the
seal-at-rest fix below. Dating it again is a release act, not a docs edit:
`.github/workflows/release.yml`'s changelog-gate now refuses to publish any
tag whose version does not match the top dated heading here, and
changelog-freshness.yml alarms if a dated heading ever again sits this long
with no matching tag. -->

### Security
- **Alert rules had no SSRF guard, and the test button was a port-scan oracle.**
  A rule's webhook URL was fetched without restriction, so an operator-supplied
  endpoint could reach link-local metadata services and private ranges from
  inside the network the server runs in, and the test button's timing and error
  text distinguished an open port from a closed one. (#607)
- **`golang.org/x/crypto/openpgp` is now pinned out of the build.** The package
  is unmaintained with no upstream fix (GO-2026-5932). It is not reachable from
  this code and the module edge is not removable — `x/crypto/acme` and
  `nacl/secretbox` are both genuinely used — so the package boundary is held
  deliberately by a test rather than left as a property of today's imports.
  (#508)

- **Two sources could share a publish token, and an encoder would land in
  whichever one the lookup happened to return.** `sources.token` had no unique
  constraint and RTMP's target map is last-writer-wins, so a duplicate admitted a
  publisher into the wrong programme — the same class of cross-programme mistake
  this release fixes elsewhere. Now a partial unique index, partial because the
  column defaults to empty and several sources legitimately have no token yet.

  **This can stop an upgrade.** A database that already holds duplicate tokens is
  REFUSED at open, before anything is written, naming the sources by id and name
  (never the token — a startup log is not a place for a publish secret) and
  carrying the exact command to clear one. That is deliberate: de-duplicating
  automatically would mean choosing which source keeps the token, and that choice
  decides which programme an already-publishing encoder lands in — a judgement
  the code has no basis for making at boot. Clear the duplicate, restart, then
  rotate the affected source's token from the UI.

  Duplicates could only arise from a client writing one, which is also fixed:
  the general update path no longer touches the token at all, so a stale browser
  tab can no longer roll a rotation back. Rotation is now the only writer. (#505)
- **A viewer's chat message could put text on a permanent third-party ban
  record.** The moderation model's own prose was passed as the ban reason to
  Twitch, and to Kick — a second sink found only when the fix was written. The
  only thing between the model and the platform was a 1000-rune truncation, so a
  crafted message wrote arbitrary text onto a moderation record under the
  broadcaster's credential, and the operator's system prompt could travel back
  out the same field. `PlatformReason()` is now a pure function of a closed
  `Category`: it takes no argument and reads no free-text field, so there is no
  expression that gets model output to a platform write. The model may not even
  claim the categories only a deterministic checker produces. Fail-open on an
  unrecognised category is kept deliberately — it is this package's tested
  contract, and inverting it was the plausible wrong move. (#495)
- **Webhook targets could reach the network the server sits on.** Validation
  checked scheme and host and stopped, so a hook could be pointed at cloud
  metadata, loopback, or a private range. Now refused at save time with a
  deliberate per-hook opt-in for self-hosted endpoints, plus a dial-time check
  that closes DNS rebinding by dialling the address it just verified. RFC6598
  carrier-NAT space — what Tailscale hands out — is refused too: `net.IP.IsPrivate`
  covers RFC1918 and IPv6 ULA and nothing else. (#489)
- **Editing a track label on one programme rewrote another's ingest.** A 500 ms
  debounce autosaved to a route that carried no source, and the handler resolved
  the default engine, so a keystroke on programme 2 rewrote programme 1 and
  restarted its live destinations. Routes now refuse rather than falling back —
  the fallback is what made it silent — and an AST test requires every caller
  reaching for the default engine to be recorded with a reason, so a new handler
  that forgets fails the build. (#497)
- **0.7.0's seal-at-rest migration left the plaintext stream keys it replaced
  legible in the database file.** The migration writes the ciphertext and blanks
  the `stream_key` column, and every check that reads the row back through SQL
  confirms it is empty. `grep` on the raw file disagrees. SQLite unlinks the
  bytes of a shortened cell without zeroing them — `secure_delete` defaults to
  **0** under `modernc.org/sqlite`, which is the driver this ships with — and it
  writes the sealed rows into the write-ahead log rather than over the old pages,
  so copies survive in both files. Measured on a 60-destination install: 60
  plaintext copies still in `polyemesis.db` after a clean-shutdown upgrade, and
  122 in `polyemesis.db-wal` after an upgrade over a server that had been killed.

  This is the one claim `internal/secrets` makes for itself — "a leaked database
  file … is not a leaked set of live streaming credentials" — and it was false
  for every install that had upgraded. No attacker is needed and nothing has to
  go wrong; it is the state a normal upgrade leaves behind.

  The database is now opened with `secure_delete` on, and the migration
  truncates the write-ahead log once it has committed. Neither half is
  sufficient alone: with the checkpoint but no pragma, two copies survived in
  freed pages; with the pragma but no checkpoint, every copy the migration had
  not yet checkpointed survived in the `-wal`.

  **The pragma only protects writes made after it is set, so an install that
  already ran the 0.7.0 migration is not fixed by upgrading.** It needs a one-off
  `VACUUM` plus `PRAGMA wal_checkpoint(TRUNCATE)` with the server stopped — see
  [UPGRADING](docs/UPGRADING.md#if-you-already-upgraded-to-070-one-off-remediation-required).
  Any backup taken between upgrading and running that scrub still contains the
  plaintext and cannot be repaired; rotate those keys instead of trusting the
  archive.

- **Twitch's negotiated ingest host was never checked, and an omitted
  `authentication` fell back to the operator's own key.** `multitrack.Resolve`
  validated that the returned `url_template` was `rtmp://` or `rtmps://` and
  that it named *a* host — never *which* host. A response carrying
  `rtmps://attacker.example/app/{stream_key}` resolved cleanly, and the engine
  replaces a destination's stored target wholesale with what Resolve returns, so
  nothing downstream reasserted the intended host. The stream key travels in the
  RTMP connect as the stream name, which means the first packet of that publish
  hands the credential over.

  The second half made it worse. `key := streamKey; if ep.Authentication != ""
  { key = ep.Authentication }` reads as a safe default: it means an endpoint
  that mints no key gets the operator's own long-lived channel credential
  instead of the per-broadcast one — while `Outcome.Use` documents the minted key
  as *mandatory*. So the hostile-host case shipped the permanent credential, not
  a disposable one.

  The host is now constrained to `.global-contribute.live-video.net`, the Amazon
  IVS contribute domain all three independently measured ingest hosts sit under,
  and a successful negotiation carrying no minted key is refused rather than
  substituted for.

- **The credential-carrying POST followed redirects.** `multitrack`'s HTTP
  client set no `CheckRedirect`, so Go followed up to ten. Go strips
  `Authorization` and `Cookie` across hosts — but the stream key is in the
  **body**, and 307/308 preserve the method and replay it. Measured: 301/302/303
  became GETs and leaked nothing; 307 and 308 delivered 684 bytes of request
  JSON, stream key included, to a second server. Every redirect is now refused.
  The same policy is reapplied to an injected client, so the test seam cannot
  silently drop it.

- **A stream key straddling the 300-byte error snippet was printed unmasked.**
  The non-2xx path read `scrub(snippet(raw), streamKey)` — truncate first, then
  look for the key — so a key beginning before offset 300 and ending after it
  was searched for in a haystack shorter than itself and never matched. Measured
  with a 55-byte key at offset 270: 30 bytes leaked, and no placeholder appeared
  anywhere in the line, so the output did not even look redacted. This is
  [#306](https://github.com/rainmanjam/polyemesis/issues/306)'s exact failure
  mode reproduced in newer code. Scrubbing is now the outermost transform, and
  the rule is written down where the next transform will be added.

- **The Twitch minted key was registered as a secret in only one of its two
  spellings.** The engine registers `Outcome.Target.Key`, which already carries
  `?clientConfigId=…`; `SecretSet.Scrub` is a substring replace, so text
  containing the key *without* its query went unmasked. What survived was
  `v1_<signature>_<manifest>_[redacted]` — the operator's original key masked
  because it is a suffix, and the signature and hex manifest left standing. The
  guard that was supposed to catch this registered the bare key, which no code
  path produces, so it passed while the shipped code leaked. `wireSpellings` now
  emits the pre-`?` prefix, symmetric with its existing control-character
  expansion and the same truncation class, and the guard registers what the
  engine registers.

- **`secret.key` was read at whatever mode it was found in.** Creating it was
  careful (`0o700` directory, `0o600` file); reading it accepted anything. A key
  file restored from a tar archive without `--same-permissions`, copied under
  umask 022, or rsynced without `-p` lands at 0644 and stayed there for ever,
  silently — and that file decrypts every destination stream key in the
  database. It is now narrowed on every successful read, the way `db.Open`
  already treats the database and its sidecars.

- **`ScrubDestinationText` collected a destination's secrets differently from
  its sibling.** The supervisor spec passes the minted key alongside the row;
  this path passed only the row, so the two answers to "which strings on this
  destination are secret" disagreed — and the shorter answer belonged to the
  function that hands text back to a caller, on its way to a **retained** MQTT
  topic that cannot be recalled. Not reachable today, because the only text that
  arrives there predates the negotiation; fixed anyway, since that is a property
  of the current callers rather than of the function.

- **A destination's stream key could reach `server.log` on the give-up path.**
  The supervisor logs the child's error twice: once per retry, and once when it
  stops retrying. The retry line was scrubbed; the give-up line four lines below
  it read the same variable raw, and that variable is FFmpeg's stderr, which on
  a refusal names the publish URL and its key.

  The give-up path is the worse of the two to leak on. It fires only after
  `MaxRestarts` **consecutive** failures — after a destination has been refused
  over and over — which is exactly the state an operator is in when they stop
  watching and start copying `server.log` into a bug report. The retry line got
  the attention because it fires often; this one fires when somebody is about to
  hand the file to a stranger.

  A fix and a test for one line of a pair, with the sibling left open, is the
  shape this file has now recorded twice.

- **A destination's stream key could reach `data/logs/process.log` in the
  clear.** When a publish endpoint refused a connection, FFmpeg printed the
  output URL it could not open — key included — and the scrubber did not remove
  it. Not because it was missing: the destination declared its key and the
  stderr path did apply the scrub. `alerts.SecretSet` replaces the EXACT
  literals it was given, and on the run this was measured on the configured
  value was 65 bytes because a terminal had appended a bracketed-paste escape
  to it, while what FFmpeg opened and printed back was the 56-byte prefix
  ending at that escape. A 65-byte needle does not occur inside a 56-byte
  haystack, so nothing matched. `process.log` is persisted and rotated, which
  makes it exactly the artifact that gets collected into bug reports and
  support bundles — and a failure is when an operator copies logs to somebody
  else. ([#306](https://github.com/rainmanjam/polyemesis/issues/306))

- **A stream key containing a control character is now refused when you save
  it, rather than silently truncated.** The other half of a publish URL was
  already protected — `url.Parse` has always rejected these bytes — but the key
  is joined on afterwards and nothing looked at it. It is refused rather than
  repaired, so what is stored is always what goes on the wire: sanitising would
  mint a credential you never typed, and a destination that publishes with a
  quietly shortened key fails with nothing to explain why. The message names
  the offending byte and its position and never echoes the key. An existing
  destination carrying such a key must have it re-entered before it can be
  saved again. Behind that, `engine.destSecrets` now also declares the prefix a
  value takes if it is truncated at a control character, so the scrub covers
  what reaches the wire and not only what was stored.

- **The multistream leak check certified clean while a key was in the log.**
  Step 8b of `scripts/acceptance-multistream.sh` searched for the exact
  configured value, so any transformation defeated it — and so did its positive
  control, which searched the same bytes. It now searches for a distinctive
  16-character prefix of each key, and a new check plants a truncated copy of
  every configured key and proves the new predicate finds what the old one did
  not. Measured: with the pre-#307 predicate restored, a truncated key planted
  in `process.log` is certified clean by 8b and caught by the new control.
  ([#307](https://github.com/rainmanjam/polyemesis/issues/307))

- **Destination stream keys are encrypted at rest.** `internal/secrets` has
  sealed every OAuth client secret and access token since it existed, and its
  own docstring says what that buys: "a leaked database file — a backup, a
  snapshot, an errant scp — is not a leaked set of live streaming credentials."
  Destination stream keys were the one credential class outside that sentence,
  sitting in `stream_key TEXT NOT NULL DEFAULT ''`, and they are the worst one
  to leave out — long-lived, rarely rotated, and worth exactly what an attacker
  wants, which is the ability to broadcast to your channel. They are now sealed
  with the same NaCl secretbox key as everything else.
  ([#297](https://github.com/rainmanjam/polyemesis/issues/297))

- **Upgrading takes no action and no downtime.** The plaintext columns are kept
  and still read, so a database written by 0.7.0 works on the first read after
  the upgrade — before the migration, in fact. The first open with a key file
  seals every key it finds still in plaintext and blanks the column it came out
  of, and it is guarded by "is there still plaintext here" rather than by a
  one-shot marker, so an interrupted upgrade finishes itself on the next start.

- **A key that cannot be decrypted disables its destination instead of
  publishing with an empty one.** If the key file is lost, replaced, or the
  database is restored onto a different machine, the affected destinations stay
  in the list with their names, platforms and routing intact and are shown as
  needing attention: *the stream key could not be read on this machine —
  re-enter it to enable this destination.* Nothing is written to the row, so
  putting the right key file back restores every one of them by itself, with no
  repair step. Re-typing the key is the other way out, and an unrelated edit —
  a rename, a routing change — deliberately leaves the sealed bytes alone
  rather than destroying a key the right key file would have recovered.

- **Go 1.26.6, for two standard-library advisories published that day.** A
  toolchain bump rather than a defect in this code, recorded here because the
  released binaries are statically linked: a stdlib advisory reaches an operator
  only when a new binary is cut, so the version the release was built with is
  part of what the release is.
  ([#329](https://github.com/rainmanjam/polyemesis/pull/329))

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
  leaving you a stopped programme and a log line. The job that writes that
  verdict — the *Media re-check* above — ships in this same release, so the gate
  is live rather than waiting on a producer.
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

- **The ingest URL in `ingest started` carried the credential, on every boot.**
  `PublicIngestURL` renders the server half only for RTMP — deliberately, and the
  comment above the caller said so — but it renders `…&passphrase=<cleartext>`
  for SRT and the operator's pull URL WHOLE for pull, and a pull URL is where a
  camera password or a CDN path token lives. Both reached `ingest started` and
  `backup ingest started` at Info, so they landed in journalctl and
  `server.log`. Debug mode's scrubbing does not cover this: the recorder is a
  second consumer and the inner handler still writes the original record. The log
  rendering now keeps the host and port — the question a failed ingest actually
  asks — and drops the rest, and an allowlist test fails the build if a third log
  line acquires the full one.

- **Facebook's credential check printed the app secret on any network hiccup.**
  The pair travels in the query string, which is load-bearing — a POST form makes
  Facebook reject correct credentials — but a transport failure arrives as a
  `*url.Error` carrying the full URL, and that was interpolated raw into the
  message an operator sees and the logs keep. A DNS outage, a timeout or a TLS
  error was enough.

- **A credential in an unrecognised attribute shape reached the debug bundle.**
  Rendering a value before scrubbing it is only safe if the rendering keeps the
  bytes. It did not, twice: a `[]byte` was base64-encoded, so the credential
  arrived present, unrecognisable to the exact-match set, and decodable by the
  recipient in one step; and `&`, `<` and `>` were escaped, so a camera or CDN
  password containing an ampersand — ordinary, and a declared literal — was
  transformed out of matching range.

- **The debug bundle's scrub set had never heard of three inventories.** It was
  built from destinations; sources were added after an earlier review; platform
  accounts (which hold the OAuth access and refresh tokens), the install-wide
  ingest, and the failover BACKUP ingest were in none of them. The backup pull
  URL is logged in full at the moment the selector switches to it — which is when
  an operator is recording, because that switch is the fault they are capturing.

- **The source credential extractor read a pull URL with the publish-URL rule.**
  `urlSecrets` says in its own comment that it "is NOT correct for a pull URL,
  where the credential is in the URL and nowhere else". It takes the last path
  segment, which for a publish URL is the stream key and for a pull URL is the
  filename, so a CDN URL was declared to the recorder as `index.m3u8` and the
  credential left in the clear.

- **The hook response pass ran backwards.** The residual `Redact` was applied
  before the declared secret set rather than after it, inverting the rule
  `internal/alerts` states about itself. `Redact` transforms text, so a declared
  literal arriving inside a URL was no longer byte-identical when the exact pass
  ran.

- **An interrupted upgrade left plaintext stream keys in the write-ahead log,
  permanently.** The 0.7.0 migration seals every row, commits, then truncates the
  log — but the function returned early when it found nothing left to seal, and
  the truncate sits after that return. An upgrade that committed and then died
  came back, found no work, and never truncated the log again. Not on that boot
  and not on any later one. Measured at 162 plaintext keys still greppable after
  the restart.

- **The checkpoint never checked itself.** `PRAGMA wal_checkpoint(TRUNCATE)` does
  not fail with an error when it cannot get the lock; it returns a row with
  `busy=1` and the log where it was. The code used `Exec`, which discards result
  rows, so the fatal-on-failure guarantee written above it was armed for a SQL
  error that cannot happen and blind to the refusal that can.

- **Exporting a debug bundle now requires a signed-in operator.** `GET /debug`
  and `PUT /debug` stay reachable by an admin API token, so a dashboard can read
  capture state and an automation can start one. Taking the file — a copy of the
  server's own logs, meant for somebody who does not have the box — joins
  `/upgrade/stage` and `/auth/tokens` behind a session.

- **Expert mode accepted `-report`.** It makes FFmpeg write its own log file
  whose first line is the argv it was invoked with, stream key included, into a
  directory nothing here scrubs.

- **`ProtectProc=invisible` on both unit definitions.** A destination's stream key
  reaches FFmpeg as a command-line argument, and on a stock Linux host
  `/proc/<pid>/cmdline` is readable by every local account. The host half is
  `hidepid=2`; see INSTALL.md.

- **The installer ran an unverified third-party FFmpeg as root.** It refuses its
  own binary without a matching `SHA256SUMS`, and fetched FFmpeg with no
  integrity check at all — then extracted it and executed it to probe for libsrt.
  It now verifies against the published `checksums.sha256` and refuses the
  upgrade if that is missing or does not match, running nothing from the
  download.

- **Renaming a webhook destroyed a signing secret that was only unreadable.**
  Two deliberate designs composed into permanent loss. The read path swallows a
  decrypt failure and leaves the secret empty on purpose — "a secret that will
  not open leaves the hook UNSIGNED rather than unreadable", so one bad row
  cannot fail the whole listing — while the write path read an empty secret as
  "keep the stored one", fetched it back, and re-sealed it. When the ciphertext
  would not open, after a restore against the wrong key or a half-finished
  rotation, the re-seal wrote a valid encryption of the empty string over bytes
  that would have opened again once the right key came back.

  Editing any other field was enough to trigger it, and the API had been
  correctly reporting `hasSecret:false` the whole time — so the operator's own
  diagnostic step was what destroyed the value. The keep path no longer
  re-seals; the stored bytes are preserved in SQL.

- **A slate colour could rewrite the filtergraph.** `SlateSettings.Color` is
  documented as "any spelling FFmpeg's colour parser accepts" and nothing
  validated it, yet it was escaped one level deep under a comment asserting its
  inputs were "validated elsewhere to be a name or 0xRRGGBB". The strict
  whitelist that comment refers to is only ever applied to rendition *text*
  colours — so the validated field got the strong escaping and the unvalidated
  one got the weak escaping. A filtergraph is unescaped twice; both slate call
  sites now use the two-level escaper and the single-level one is gone.

- **The transcription confinement check did not confine.** A recording name is
  refused if it contains a separator or `..`, and the joined path is then tested
  for being inside the recordings directory — using an absolute-path conversion,
  which does not follow symlinks. A symlink placed in that directory passed
  every check, and both the existence test and the reader after it traversed to
  the target. Links are now resolved before the test, on the directory as well
  as the file, so an install whose recordings path is itself a symlink still
  works.

### Added
- **Trovo signs in.** OAuth 2.0 authorization code (no PKCE — Trovo documents
  none), the **stream key** over `channel_details_self`, title and category
  push over `channel_update_self`, and the live viewer count off the same
  channel response the key comes from. Trovo publishes no broadcast object at
  all, so start/end is *Not possible* there exactly as on Twitch and Kick.
  Chat and moderation are documented and deliberately not built yet; their
  scopes are not requested until they are.
- **A platform may now supply the stream key without an ingest URL.** Trovo
  issues its ingest hostname per region and publishes it nowhere in its API, so
  the server URL is copied out of the creator dashboard once. `Refresh stream
  key` keeps whatever URL the destination already had rather than blanking it,
  and says which field to go and fetch when there is none — previously it
  overwrote unconditionally, which for a platform like this turned a working
  destination into "an RTMP URL is required".
- **Every figure on every screen now says what it means.** Sixty-eight readings
  across twelve pages named a quantity and nothing else: `Speed 2.17x`,
  `Deviation — LU`, `Relay drops`, `PID`. Each now carries an explanation on
  hover — what it measures, and what a bad value would look like. It is not a
  convention that could be forgotten next time: a figure without an
  explanation no longer compiles.
- **The audio chips on a destination card name their tracks.** Six identical
  numbered squares said which tracks a platform receives and nothing about what
  those tracks are, so checking that the podcast feed carries the mic and not
  the music meant holding the routing editor's ordering in your head. They now
  read `Track 3 (Commentary) is included`, using the editor's own names.
  Excluded chips are named too — the question is usually "why is the music
  missing?". On a multi-programme install a card whose programme is not the
  selected one keeps the plain wording rather than borrowing another show's
  names.
- **Icon-only buttons and status dots answer on hover.** Every icon button
  already told a screen reader what it does and told a mouse user nothing;
  sixty-two of them now say the same sentence to both. The status dot carried
  its meaning in colour and shape alone — readable only if you already knew the
  vocabulary, and invisible to a screen reader — and now names its state.
- **The mix matrix says which track and which output each cell is.** The grid
  described every cell for a screen reader and showed everyone else an
  unlabelled box of digits, in a table whose row and column headers scroll out
  of view. Cells name the track by name where the ingest provides one.
- **The dashboard says when a disconnect would end the broadcast.** Failover is
  the device that prevents the one unrecoverable failure here — a completed
  YouTube broadcast cannot return to live — and it is off by default for good
  reasons that do not include "nobody should find out it exists". Once a
  programme has an enabled destination and failover is off, the destination
  list says so, and links to the setting. It clears itself when the setting
  changes rather than offering a dismiss button, so what is on screen is always
  the current exposure. (#512)
- **A lane per programme, so position says which is which.** Headings put a
  programme's name above its destinations but left its preview elsewhere on the
  page, so answering "is THIS one on air, and where is it going?" meant reading
  a name in two places and trusting they matched. A lane answers it by position:
  the picture and the destinations carrying it are the same box. (#483)
- **Destinations grouped under the programme they carry.** (#481)
- **The programme a destination carries is chosen, not guessed.** Creating a
  destination on a multi-source install silently attached it to whichever source
  the server picked. (#479)
- **A preview per source, following what is on air, that stops when unwatched.**
  (#472)
- **The second (VOD) audio mix has a UI.** `vodProfile` shipped complete — the
  column, the migration, the API, `routing.CompilePair`, the engine's gate on
  Twitch's one-track ingest — and appeared in the frontend exactly once, as a
  type on line 268 of `types.ts`. An operator could switch Enhanced Broadcasting
  on, watch it negotiate, and have no way to say what the second mix *contained*;
  the feature was API-reachable only.

  It is now edited on **Routing → Second (VOD) audio mix**, and it is the SAME
  editor the live mix uses rather than a second one written beside it. Both are
  a `routing.Profile`, so the block under the destination picker was extracted
  into one `ProfileEditor` component and is rendered twice — same track picks,
  mix matrix, music rights, loudness, delay, ducking, presets, and its own
  compiled filter graph from the same Go code that will run. Two things had to
  change to make it reusable: every DOM id is now namespaced (`id="norm"` became
  a duplicate the moment there were two editors, so clicking the second one's
  label moved the first one's control), and each instance compiles itself
  instead of borrowing a result that is not its own.

  Off is the default and stays the default. Switching it on seeds the second mix
  from the live one, so the first edit is the actual difference. Switching it
  off sends an explicit `null` — the API decodes over the stored row, so an
  omitted field would leave the pointer alone and the operator would watch their
  delete undo itself on the next load.

  The editor is **not** gated on Twitch: `routing.CompilePair` →
  `engine/destinations.go` → `ffmpeg.secondAudioMap` is a real two-mix egress
  and correct for every other target. What is gated on Twitch is the
  *explanation*. Where Enhanced Broadcasting is off on a Twitch RTMP
  destination, the engine refuses the pair at plan time and the page says the
  second mix is **not being sent**, in those words. Where it is on, the page
  says the answer is decided at go-live and lands on the destination card — it
  does not claim a second track that no negotiation has granted yet.

- **Twitch Enhanced Broadcasting is now reachable. EXPERIMENTAL: no broadcast
  has ever been published through a key it minted.** The negotiation is not the
  gap — `internal/multitrack/live_test.go` reaches `ingest.twitch.tv` on every
  run and Twitch answers: a supported-GPU inventory is accepted, a VOD audio
  track is granted, and a key is minted. What has never been observed is
  anything after that. No second audio track has been seen arriving at Twitch,
  and `internal/engine`'s wiring around the negotiation has only ever been
  driven by an `httptest` server; watching it end to end needs a real stream key
  and a real broadcast. Nothing is gated on that label: the feature is linked in,
  the toggle is one click, and a negotiation that does not succeed falls back
  to the ordinary Twitch ingest — the path every install used before this
  existed. `internal/multitrack` was
  complete, documented and tested, and nothing imported it: `go list -deps
  ./cmd/polyemesis` did not include the package, the `multitrack` column on a
  destination was written by the API, migrated, persisted, scanned back and
  shown as a toggle, and no code path ever read it. It is wired into the
  go-live path. A destination that opted in negotiates once per start — not per
  reconcile and not per reconnect — and on success publishes to the ingest
  Twitch names with the 312-character key Twitch mints, which carries the
  agreed rendition ladder signed inside it. That key is registered as a
  credential in its own right rather than assumed covered by the operator's:
  the minted value ENDS WITH the original, so a scrubber that knows only the
  original masks the last segment and leaves the signature and the manifest
  standing in `process.log` — a partially redacted live credential, which is
  the shape of #310 and #324.

  The hardware the negotiation needs is DECLARED under Settings → Pipeline →
  Enhanced Broadcasting hardware, not detected. Twitch validates the inventory
  and refuses by name on a zero vendor ID, an unrecognised vendor and an
  out-of-date driver; polyemesis can measure one of `multitrack.GPU`'s six
  fields on one platform, so a request assembled from that one with zeros in
  the rest would describe a machine that does not exist. With nothing declared
  no request is made at all, which is the state every install starts in and is
  not a fault: the destination publishes to the ordinary Twitch ingest and says
  so once, on its card, in plain text rather than behind an alert triangle.
  A refusal, a 5xx, a timeout and a hung platform all reach the same place.

- **A second audio mix is no longer pushed silently at an ingest that takes
  one.** The engine compiled the VOD pair on `destination.vodProfile` alone and
  never read `destination.multitrack`, so a Twitch destination with a second
  mix configured sent TWO audio tracks to an ingest documented as carrying one
  — with nothing in the log, the status or the card saying so, while
  `db.Destination.VODProfile`'s own comment promised "the engine reports it".
  It reports it now, in both directions: the pair is refused outright at plan
  time on a Twitch destination that did not opt in, and dropped at go-live on
  one that did and whose negotiation did not succeed. Both say why. The
  redundant feed of such a destination carries one track too, because it
  publishes to the operator's own backup URL whatever the negotiation said.
  The generic two-mix egress to every non-Twitch target is untouched.

- **A platform registry seeded from OBS's own service list**, carrying each
  destination's ingest hosts, its maximum video bitrate and its capability row.
  The Kick preset shipped with a real per-channel ingest host in a shape that
  could not publish, and six presets had no capability row at all — both
  invisible until something tried to use them.
  ([#312](https://github.com/rainmanjam/polyemesis/issues/312))

- **Rumble is a fifth chat platform.** Its live-stream API does carry chat,
  which the previous survey had concluded it did not.
  ([#321](https://github.com/rainmanjam/polyemesis/pull/321))

- **RTMP egress can carry a second audio track, and it has been measured doing
  it.** `ffmpeg.DestSpec.SecondAudioOutLabel` names a second finished mix from
  the destination's filter graph; it is mapped and encoded as a second audio
  track alongside the first. One track remains the default and every existing
  destination emits byte-for-byte the command it emitted before — no caller sets
  the field yet, because `routing.Compile` still describes one mix per
  destination. What is new is that the capability is no longer a guess: FFmpeg
  8.1 muxes two AAC tracks into FLV as Enhanced RTMP multitrack, this project's
  own RTMP server carries them, and
  `internal/ffmpeg.TestTwoDistinctMixesReachAnRTMPFarEnd` publishes the built
  argv into that server and reads a 300 Hz tone off one received track and a
  5000 Hz tone off the other. Tones rather than a track count, because two
  tracks carrying the same audio is a failure a count cannot see — and the same
  test proves it can tell, by publishing one mix twice and asserting the
  difference is absent. Whether any PLATFORM accepts a second audio track is a
  separate question this does not answer; the refusal of `-c:a copy` on an RTMP
  destination, which rests on that question, is untouched.
  ([#141](https://github.com/rainmanjam/polyemesis/issues/141))

- **`internal/multitrack` speaks Twitch Enhanced Broadcasting**, the negotiated
  configuration behind what Amazon calls IVS Multitrack Video — the one path a
  platform has published that takes a *second* audio track and says what it is
  for. It fetches a configuration from Twitch, reads the verdict, and resolves
  the ingest endpoint and stream key it hands back. Nothing publishes through it
  yet; see below for what is deliberately not built.

  **EXPERIMENTAL:** the four measurements below come from responses Twitch sent
  to this package's own requests, and `live_test.go` asks for them again on
  every run rather than replaying a capture — so a change at the far end shows
  up as a failing test rather than as a fixture that has quietly stopped being
  true. A declared GPU inventory does get past Twitch's hardware check. What is
  untested is everything after the negotiation answers: no key minted here has
  ever carried a broadcast.

  Four things were measured against the live endpoint rather than assumed, and
  each one changes what the code has to do:

  - **A refusal arrives as HTTP 200.** Every response — valid, invalid,
    unsupported hardware, unparseable schema version — was `200`, with the
    verdict in `status.result`. A successful negotiation omits the `status`
    object entirely rather than saying `"success"`, so a client that reads the
    status code, or that treats an absent status as an error, has read the
    wrong field.
  - **`authentication` is the stream key, not an OAuth token.** This does not
    depend on a connected account, which is what the issue expected. Better:
    on a *successful* negotiation Twitch mints a new 312-character key that
    carries the agreed ladder inside it, hex-encoded and signed, with the
    operator's original key as its last segment. Publishing with the operator's
    own key instead would connect and send a stream the ingest never agreed the
    shape of.
  - **A second audio track does not require a multi-rendition video ladder.**
    Asking for `maximum_video_tracks: 1` returns exactly one rendition *and*
    both audio tracks — live on track 0, VOD on track 1. That is what makes the
    feature reachable at all for polyemesis, which publishes one video track to
    an RTMP destination.
  - **Twitch refuses a client with no supported GPU**, by name: no GPU
    information, a vendor ID of zero, an unrecognised vendor, an out-of-date
    driver. There is no software-encoder path through this endpoint, so on a
    headless host encoding with libx264 the fallback to the ordinary ingest is
    the *normal* outcome and not the exceptional one.

  The operator's own settings are **the input to the negotiation, not something
  it overrides** — the returned ladder is derived from the canvas the client
  says it is producing, so an operator who picks 720p gets a 720p negotiation.
  Where Twitch's answer differs anyway (a `maximum_aggregate_bitrate` ceiling
  was simply ignored), the difference is reported and never silently applied,
  following the rule already written into `services.URLProblem`.

  Both the request and the response carry a credential, so neither may be
  logged as it stands: `Config.Redacted` is the only shape of a configuration
  fit to print, and every error the client returns is scrubbed of the key.
  ([#326](https://github.com/rainmanjam/polyemesis/issues/326))

- **Capped VBR: a rendition can now set a bitrate ceiling and a rate window.**
  `RenditionSpec` had carried `MaxrateKbps` and `BufsizeKbps` since it was
  written, and `RenditionArgs` used both correctly — but nothing could set
  them. `renditionSpecOf` mapped `VideoKbps` and stopped, so every install fell
  through to the CBR relationship whatever the operator intended. Nothing was
  broken; the code described a capability the product did not have, which is
  the harder kind to notice because every individual piece passes its own test.

  A ceiling **below** the target bitrate is refused rather than clamped. There
  is no way to resolve `-b:v 6000 -maxrate 4000` without overriding one of the
  two numbers, and whichever is chosen the operator gets a stream at a bitrate
  they did not pick with no sign that a field they filled in was ignored. Both
  fields default to `0`, which emits byte-for-byte the command line every
  existing install already emits.
  ([#341](https://github.com/rainmanjam/polyemesis/issues/341))

- **The first-run screen states what the admin password does and does not
  protect.** A strength meter now scores the password as it is typed, and the
  notice beside it corrects an assumption the screen previously invited: this
  password protects the admin UI and the API, and it is **not** what encrypts
  your stream keys. Those are sealed with a key file in the data directory,
  which must be backed up alongside the database or every destination comes
  back disabled after a restore.

  The key is deliberately not derived from the password, because the server
  refreshes OAuth tokens while nobody is logged in. Deriving it would be
  security theatre that also broke unattended restarts.
  ([#346](https://github.com/rainmanjam/polyemesis/pull/346))

- **An upload the server never managed to inspect can be re-checked in place,
  instead of being re-uploaded.** `POST /media/{name}/verify` queues a *Media
  re-check*: it reads the stored bytes again and records what it actually
  established. Both directions are reported, because both matter to somebody —
  a file that was refused and now passes has just become usable as a playlist
  item, and one that passes and now fails has just stopped being usable. A
  second refusal repeats the reason rather than silently re-asserting the first.
  The route takes the same two checks as deleting an upload, in the same order,
  so a probe sidecar cannot be named as the thing to verify.
  ([#202](https://github.com/rainmanjam/polyemesis/issues/202))

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

  This state was added before anything wrote it, because a re-verify that
  recorded its findings as "not checked" would be worse than no re-verify at
  all. The job that writes it — the *Media re-check* under Added — ships in this
  same release, so `refused` is a value you can now actually be shown.
  ([#202](https://github.com/rainmanjam/polyemesis/issues/202))

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

- **One control starts or stops every destination.** An operator with eight
  destinations was pressing eight buttons; `POST /destinations/start-all` and
  `POST /destinations/stop-all` now act on the whole install, with a matching
  pair of buttons beside the destination list on the dashboard. There is no id
  list and no per-card selection: the routes act on everything, deliberately,
  because a bulk control with a selection is the per-destination control with
  extra steps and one more thing that can be stale by the time it is pressed.

  Each row is driven through the same code as the per-destination start and
  stop, so the bulk control can never be more destructive than the button it
  replaces — and **the answer is a list, never a boolean**. One row per
  destination, naming which it was, what happened (`started`, `stopped`,
  `warned`, `failed` or `skipped`) and why when something did not happen. Eight
  destinations of which two refuse is not "failed", and the operator should not
  have to open eight cards to find out which two.

  **Starts are paced**, one destination at a time with a gap between them, so a
  burst of encoder processes does not contend on the box and the same
  connections do not arrive at a platform as one clap. That is a pacing choice
  about this machine; it encodes no platform's published ceiling, counts
  nothing and caps nothing. Stops are not paced — tearing down is local.

  Stopping asks for confirmation, and the confirmation says what stopping
  actually costs: **stop and disable are one thing on the server**, so stopping
  ends every YouTube broadcast on the install and a completed YouTube broadcast
  cannot return to live. Starting again puts the video back on the wire; it does
  not bring the broadcasts back. That was already true of the per-destination
  Stop button one row at a time — the wording is new, not the consequence.

- **The viewer count is on screen, and a withheld one does not read as zero.**
  Three platforms implement `GET /platforms/accounts/{id}/stats`, the route
  answered, and nothing in the UI called it — so the capability matrix said
  "Viewers: Works" while no operator could see a number anywhere. Every
  connected account in Settings → Platforms now carries its live state, polled
  once a minute while the tab is visible and stopped entirely for a platform
  that has said it cannot answer.

  The distinction the round is actually about: a live stream whose count the
  platform DECLINED to give reads "Viewer count not reported", never `0` and
  never a dash. YouTube omits the number when nobody is watching, when the owner
  has hidden it, and once the broadcast ends — three states, one absent key —
  and the one that matters is the streamer with an audience who would otherwise
  be told nobody is there. A reported `0` still renders as `0`, because on a
  live stream that is a fact. A platform polyemesis cannot ask shows the
  server's own sentence naming it rather than an empty space, and an offline
  channel says offline rather than reporting an audience of none.

  The interval is a quota decision. One YouTube stats read is three requests
  against a project-wide ceiling of 10,000 units a day that title push,
  compliance and chat all draw from, so polling harder does not merely slow this
  panel down — it takes metadata push down with it.

- **polyemesis.com publishes the documentation.** The site went from 6 pages to
  37: the 23 user-facing documents in `docs/` are rendered at `/docs/<slug>`
  rather than linked to GitHub, five comparison pages sit under `/vs/`, and
  `/free-restream-service` and `/how-to-multistream-from-obs` answer the two
  queries with the most measured search demand this project can address.

  The documents were invisible to search before this — 63,000 words reachable
  only on github.com, so the authority accrued to Microsoft's domain and the
  pages had no title, description or internal linking under our control.

  Publishing is an **allowlist**, not a glob with exclusions. `docs/` also holds
  `RESEARCH-COMPETITIVE.md` and `COPY-CONSTRAINTS.md`, and a glob would make the
  next internal note public by default. A build check fails when a file in that
  directory appears in neither list, so adding a document is a decision somebody
  writes down.

- **`llms.txt`, `/.well-known/security.txt`, an apple-touch-icon, `HowTo`
  structured data on the install steps, and `lastmod` in the sitemap.** The
  `lastmod` dates come from `git log -1` per page rather than the clock: a
  build-time stamp marks every page as modified on every deploy, which is a lie
  a crawler discounts, costing the signal it was meant to give.

  `security.txt` carries the mandatory RFC 9116 `Expires` field, and the build
  now fails when it is past and warns 30 days out. An expired security contact
  reads as an abandoned one to the person deciding between reporting privately
  and going public.

- **Broadcast lifecycle: polyemesis can tell YouTube to go live and to end.**
  Connecting an account used to mean fetching a key and pushing a title;
  "going live" was still bytes arriving at an ingest. A coordinator now drives
  the platform's own broadcast state from the UP/DOWN edges the engine already
  derives, and the `broadcastLifecycle` column says per platform what that
  actually means. **YouTube is driven** — it goes live when video starts
  arriving and ends when you disable or delete the destination, never when the
  encoder merely crashes, because a completed YouTube broadcast cannot return
  to live and a crash is recoverable. **Facebook is commanded by hand**:
  connecting creates the live video and "End broadcast" is on the destination
  menu, but nothing ends it for you. Twitch and Kick publish no such API at
  all, which is established by enumeration rather than assumed.

  A refused transition raises a fault and never stops the stream. Stopping the
  encoder on a failed transition would destroy the only condition under which a
  retry could succeed, since YouTube requires an active ingest to transition.

- **Facebook's stream health and end-broadcast are wired to routes.** Both
  existed in the provider and neither had a route, so the menu item would have
  404ed while the stream stayed live. The health pane also states that Twitch,
  YouTube and Kick publish no ingest numbers at all — that absence is a fact
  about those platforms, not a gap in polyemesis.

- **The documentation site renders diagrams.** `docs/INSTALL.md` gained
  flowcharts of the installer and of the FFmpeg gate, and `/docs/install`
  draws them instead of printing forty lines of source at the reader.

- **A documented way to remove polyemesis.** `docs/INSTALL.md` had no "Removing
  it" section, and binary installs shipped no uninstall script, so the only
  removal instructions were the ones an operator worked out themselves. Both now
  exist, and the uninstall path is exercised in CI rather than described.

- **The non-systemd caveat, written down.** polyemesis puts each FFmpeg child in
  its own process group so that stopping one destination stops its helpers and a
  Ctrl-C reaches polyemesis rather than killing its children mid-write. The cost
  of that isolation is that a polyemesis which is *killed* rather than asked to
  stop signals nothing on the way out, and its encoders keep running, reparented
  to `init`, still holding the relay ports they were reading. Under the shipped
  unit `KillMode=mixed` closes that — systemd SIGKILLs the whole cgroup. Outside
  systemd nothing does, and now `INSTALL.md` says so next to the `KillMode` entry.
  ([#448](https://github.com/rainmanjam/polyemesis/issues/448))

### Changed
- **The uninstaller refuses while a broadcast is on air.** Stopping the service
  ends every live broadcast on the install, and a completed broadcast cannot be
  returned to — yet the installer asked "Proceed?" before the reversible act of
  installing while the uninstaller asked nothing before the irreversible one.
  All three uninstallers (systemd, Docker, Windows) now refuse while the service
  is publishing, name what is live, and require the service name typed to
  confirm. A run with no terminal is refused rather than assumed, so an
  unattended job cannot uninstall a broadcast server by inheriting the script.
  `--remove-data` performs the deletion with guards instead of printing an
  unguarded `rm -rf` for the operator to paste.
- **A version tag cannot publish a release the changelog does not describe.**
  `release.yml` gained two gates: one requiring a successful CI run against the
  tagged commit, and one requiring the tag to match the top version heading in
  this file, dated. Both fail closed — an API error, an unparseable changelog or
  an undated heading all refuse rather than guess. (#499)
- **Two features are now labelled EXPERIMENTAL throughout — labelled, not
  gated.** Twitch Enhanced Broadcasting and hardware encoding both shipped in
  0.7.0 with a gap in the evidence behind them, and the label names where each
  gap actually starts. For Enhanced Broadcasting it is *after* the negotiation:
  `internal/multitrack/live_test.go` reaches `ingest.twitch.tv` on every run and
  Twitch accepts a supported-GPU inventory, grants a VOD audio track and mints a
  key — what has never been observed is a broadcast published through that key,
  and `internal/engine`'s wiring around it has only ever been driven by an
  `httptest` server. For hardware encoding it is the `_nvenc`, `_qsv`, `_vaapi`
  and `_amf` flags — eight of the twelve encoder profiles — which were read off
  FFmpeg's option tables inside a GPU-less container. Both remain fully enabled,
  nothing is hidden, no opt-in env var or feature flag exists, and every control
  works exactly as before.

  **The first version of these labels was wrong in both directions, and only
  running something found it.** They asserted that the negotiation had never
  reached Twitch while a non-skipping test in the tree reached it on every green
  build, and they warned a Mac operator off `h264_videotoolbox` — the one
  hardware family a real encode confirms. A claim about what has *never* been
  tested can only be checked by running the test.

  One convention, applied everywhere, and each use names the specific untested
  claim rather than saying "beta": a `<Experimental>` component in the UI
  (`ui/src/components/Experimental.tsx`), an `// EXPERIMENTAL: <what is
  unverified>` line in Go doc comments, a `> **EXPERIMENTAL — <claim>.**`
  blockquote in docs, and a leading `**EXPERIMENTAL.**` sentence on a changelog
  entry. The badge deliberately uses the neutral `outline` variant: the app's
  saturated tokens mean the state of a *destination*, and "unverified on
  hardware" is a property of the feature.

  This also corrects one claim that read as stronger than it was.
  `encoderProfiles`' doc comment said the values were "verified by running
  `ffmpeg -h encoder=<name>` and a real one-frame encode" — true, but the option
  table answers on a machine with no such device, and the one-frame encode only
  ran for encoders the container could open. The narrower evidence that *does*
  exist is now named where the table lives:
  `TestEveryConfiguredEncoderOpensWithItsOwnFlags` runs a real encode per
  registered encoder with that encoder's own row — capped-VBR path included —
  and answers for whichever encoders the machine running it registers. A CI
  runner with an NVIDIA card would retire the NVENC caveat by itself.

  The UI badge is gated on the encoder *family* rather than on
  `encoder.hardware`, which is what made it fire on VideoToolbox.

- **Documentation-only pull requests no longer run the acceptance matrix.**
  Three documentation PRs in one day each fired all 27 checks — the full
  acceptance matrix, three-OS builds, Docker and browser suites — to validate
  files no job reads. One of them was a single file. The gate fails toward
  running: `code` is false only when every changed path is documentation, so a
  new top-level directory gets the full matrix until somebody decides otherwise.

  The first attempt at this made every documentation PR **unmergeable**, and the
  mechanism is worth recording because it is not obvious. Twelve acceptance
  suites are required status checks. A skipped *ordinary* job still reports, and
  a skip satisfies the requirement — but a skipped **matrix** job never expands
  its matrix, so the per-leg contexts are not skipped, they are never created.
  The checks list showed one entry named literally `acceptance: ${{
  matrix.suite }}` and twelve required contexts reported missing. The matrix now
  always expands and the gate is on each step instead, so a documentation run
  costs a runner allocation rather than a suite.
  ([#350](https://github.com/rainmanjam/polyemesis/issues/350))

- **The `internal/api` coverage guard could not fire before the ceiling above
  it.** Its three probes carried no `-timeout`, so Go's ten-minute default
  applied to each — thirty minutes of worst case behind a job ceiling of
  twenty-five. It could never have reported; it could only ever have been
  cancelled, which is what happened when it hung for twenty-four minutes against
  a step measured at 98–114 s. Bounded now at four minutes per probe with a
  step timeout above that, so Go's own panic still wins the race and names the
  test that was running.
  ([#357](https://github.com/rainmanjam/polyemesis/pull/357))

- **The marketing site moved to Cloudflare Workers Static Assets**, because
  Cloudflare stopped offering new Pages projects.
  ([#332](https://github.com/rainmanjam/polyemesis/pull/332))

- **The marketing site gained a screenshot lightbox, a version in the footer, a
  "who this is not for" section, and touch affordances** for the scrollable
  comparison table that previously only signalled to a cursor. Screenshots were
  re-shot at 61% smaller with no loss of resolution — quantisation in the
  capture pipeline rather than downscaling, which was measured making PNG
  compression *worse*.
  ([#335](https://github.com/rainmanjam/polyemesis/pull/335),
  [#336](https://github.com/rainmanjam/polyemesis/pull/336),
  [#337](https://github.com/rainmanjam/polyemesis/pull/337),
  [#338](https://github.com/rainmanjam/polyemesis/pull/338),
  [#345](https://github.com/rainmanjam/polyemesis/pull/345))

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

- **An install with no source is one somebody can actually use.** A fresh
  database no longer manufactures a "Main" source nobody configured, so zero
  sources is a normal state rather than a state the product had never been run
  in — and every screen now behaves as though it were. Reads answer (the
  dashboard, the telemetry socket and the Prometheus scrape used to nil-deref
  together on the very first page load); the routes that act on a pipeline
  refuse with `503` and `code: "no_source"` rather than falling over; and the
  Dashboard, Sources and Settings pages draw an empty state naming the one next
  action instead of a red toast.

  Two things that were previously silent are now loud. The settings **ingest**
  editor refuses a change on an install with nowhere to write it through to,
  where it used to store the block, answer `200` and have no effect whatever;
  every other setting in the same request is still saved, because `PUT
  /settings` also holds the listeners, recording, chat, automod and alerts, all
  of which an operator legitimately configures before creating a source. And
  the startup banner says `ingest no programme yet` instead of naming a port
  that is bound and will refuse the encoder aimed at it.

  Upgrading installs are unaffected: the migration still carries an existing
  single-ingest configuration onto a source called "Main", and now tells that
  case apart from a first run rather than seeding both. (#387)

- **The listener ports stay editable on an install with no source.** They belong
  to the whole install rather than to a programme — one SRT listener serves
  every source and tells them apart by publish token — so they lived on the
  ingest tab, which the empty state had replaced wholesale. A first install
  whose 1935 or 6000 was already taken therefore had no port control anywhere in
  the UI, and was invited to create a source that would arrive on the port that
  could not bind. (#387)

- **`POST /alerts/rules/{id}/test` says which absence it is refusing for.** It
  answered `503 the alert notifier is not running` on an install with no
  programme, which sent the operator looking for a subsystem to restart:
  `Engine.Alerts()` is nil for exactly one reason, and it is this one. It now
  carries `code: "no_source"` like every other refusal of the same condition.
  (#387)

- **The startup SRT warning names a screen that exists on the boot that prints
  it.** "switch Settings → Ingest to RTMP" was executable only because a seeded
  source put an ingest form on that tab. `docs/QUICKSTART.md`, `docs/INSTALL.md`,
  the meters page and the media uploader carried the same kind of pointer and
  now say where the setting really is. (#387)

- **The last source can be deleted.** The store refused it, on the grounds that
  an install with none had no ingest and no way through the UI to get one back.
  Neither half holds any more: zero sources is the state a fresh install boots
  into, and the Sources page carries the form that ends it. The refusal was
  standing between an operator and a place they can already be — most obviously
  the one replacing their single source, who had to create the replacement
  first and then work out which of two rows was which. The delete button on the
  only source is no longer greyed out, and its confirmation says what the
  install is left with: destinations and renditions go with the source,
  recordings stay on disk, and nothing is publishing until another source
  exists. A boot after that delete does not put "Main" back. (#387)

- **The confirmation of a deleted source stopped disagreeing with the warning
  that preceded it.** The dialog said renditions go with the source, which they
  do; the message afterwards named destinations alone, so the operator who
  wanted to keep a 720p encode for a replacement was told nothing had been lost
  that they would have to build again. Both now name the same three things, and
  deleting the LAST source says so — that message is the only thing on screen at
  the moment the install stops having a programme. (#387)

- **Install-wide state comes off the engine** (first of six changes for #387).
  Tools, recording paths, the host sampler and the settings read no longer route
  through whichever engine happens to be default. No behaviour change intended,
  with one exception worth stating: `GET /system` now reads settings from the
  store rather than the engine's snapshot, so it stops lagging a settings save.

- **An onboarding tour, offered once per install rather than once per browser.**
  A new operator finishes the signup screen and lands on an empty dashboard, and
  everything they need next is either in a terminal they have closed or in
  `docs/`. The tour covers the things the console does not say out loud: where a
  source's publish address and token live now that `install.sh` has printed them
  and exited, that Routing is the part which is not restreaming, what the
  `experimental` badge is actually claiming, and that `secret.key` is a separate
  file whose absence brings every destination back *disabled* after a restore
  with nothing on screen to explain why.

  It is **offered, not launched**: a dismissible strip under the header, plus a
  replay control in Settings. Completion is stored server-side —
  `users.tour_completed_at`, added by an idempotent migration — so an operator
  opening the same install from a second machine is not offered it again. New
  routes: `GET /api/v1/tour` and `POST /api/v1/tour/complete`, the write
  admin-scoped because a read-only token must not mutate user state.

  Built on [driver.js](https://github.com/kamranahmedse/driver.js) (MIT, **zero
  dependencies**), themed from `ui/src/index.css`'s own tokens rather than the
  library's stock white popover. A tour's failure mode is silent — a selector
  that stops matching simply highlights nothing — so the steps are DATA in
  `ui/src/lib/tourSteps.ts` and `ui/src/lib/tour-drift.test.ts` asserts every
  selector against the component that owns it, reading past comments so an
  anchor cannot be kept green by leaving the words behind in one.

- **A keyframe lookup that finds nothing now widens to a bounded window rather
  than reading the whole file.** The fallback asked for one packet record per
  frame across the entire recording and buffered all of it, on a path an
  authenticated request can reach — so asking about a few seconds of a long
  archive allocated in proportion to the archive. Both cases the fallback exists
  for, a long GOP and a file shorter than the lookback, are covered by a
  ten-minute window centred on the point in question.

### Fixed
- **The retention sweep could delete the recording being written.** The age
  branch removed anything past the cutoff with nothing protecting the open
  segment, which a segment length longer than the max-age reaches in the ordinary
  course of things — and the sweep runs every thirty seconds, so it would find it.
  Both branches now skip the live segment by filename. The size cap's existing
  guard was positional rather than identity-based — it protected whichever row
  sorted last — so it has been replaced rather than copied. (#504)
- **Two goroutines racing could kill the process and end every live broadcast.**
  A subscriber's `close()` was select-then-close, which is check-then-act rather
  than atomic; three teardown paths call it and nothing in the package recovers,
  so the second close panicked the daemon. A per-instance `sync.Once` makes the
  double close unrepresentable. (#496)
- **A rollback to 0.6.x would have silently dropped every destination.** 0.7.0 is
  the first release that seals stream keys at rest, so an older binary would open
  the database, read an empty key and publish nowhere, with nothing refusing and
  nothing explaining. The schema is now stamped with `PRAGMA user_version` and a
  database written by a newer release is refused before any write, naming both
  versions. This protects rollbacks *from* 0.7.0 onward; it cannot protect the
  0.7.0→0.6.x rollback itself, because the binary doing the reading is already
  shipped. (#498)
- **The upgrade path was not exercised by anything.** A column declared only in
  `schema.sql` reached fresh installs and not upgraded ones, because the file is
  `CREATE TABLE IF NOT EXISTS` — so every hook read on an upgraded install
  answered "no such column" while the whole test suite stayed green, since every
  test builds a fresh database. Migration added, and with it a test that creates
  a row, drops the column, asserts the database is unreadable in that state, then
  migrates and checks the row survived. (#489)
- **The Windows job-object setup handed the kernel an address the Go runtime was
  free to invalidate, which is a write into freed memory.** `ensureJob`
  converts `&info` to a `uintptr` and passes the integer to
  `windows.SetInformationJobObject`. unsafe's rules permit that only where the
  compiler arranges for the object to be *retained and not moved* until the call
  completes, and it does not do so here: the x/sys wrapper is an ordinary,
  splittable Go function that happens to take a `uintptr`. The
  `runtime.KeepAlive` added earlier covered retention only —
  `KeepAlive` is deliberately special-cased *not* to force its argument to
  escape, so the struct stayed a stack local, and any stack growth between the
  conversion and the syscall relocates it while the integer keeps pointing at
  the freed old stack. The kernel then writes 112 bytes into memory the runtime
  has already recycled, and the crash lands later in an unrelated goroutine as
  `fatal error: found pointer to free object`. The conversions now sit in the
  argument list of `//go:uintptrescapes` wrappers, which forces the struct onto
  the heap — where Go's collector never moves it — and retains it for the
  duration of the call. No tool in the toolchain flags the unfixed form, so the
  guarantee is now asserted by reading the compiler's escape analysis back out
  in a test that runs on every platform, not just Windows. (#440)
- **The destination hold was bounded on a clock nobody was measuring.**
  `reconcileOutputs` holds every destination until the ingest layout is probed,
  and its only exit was five CONSECUTIVE probe failures. But `probeLoop` probes
  only while the relay is carrying data, so the counter advances during flowing
  stretches and freezes, unreset, during quiet ones — and a relay that goes
  quiet and back is routine in an encoder's first minute, because the selector
  leaves the primary for the slate and returns. The exit therefore needs 65s of
  FLOW, and the wall-clock cost is that plus every quiet spell in between,
  without limit. Adds a wall-clock ceiling set above the counter's own worst
  case, so on a stream being probed the counter still reaches five first and the
  ceiling only bites where the counter is blind. (#473, #485)
- **A destination was answered for by the first programme's engine rather than
  its own.** `s.eng()` is `mgr.Default()`, which is `engines[0]` — correct only
  on a single-source install. (#478)
- **47 mistake-proofing defects, and the toolchain they were found on.** (#480)
- **The upgrade guard refused upgrades it was written to protect.** The
  generated `update.sh` checked its backup with `tar tzf … | grep -q secret.key`
  under `set -o pipefail`. `grep -q` exits on its first match, `tar` then takes
  SIGPIPE, the pipeline returns 141, and `if !` inverts that into "the file is
  missing" — so a backup that **did** contain `secret.key` was rejected with
  *"Refusing to upgrade."*

  It is not a race and not a rare edge. The determinant is the pipe buffer:
  below roughly 200 entries `tar` finishes writing before `grep` exits and the
  check passes; above it, `tar` is still writing and takes the signal. Measured
  in `debian:bookworm-slim` — 0, 10 and 200 extra files pass, 2000 fails — so it
  would have blocked the upgrade on essentially every install with a real
  recordings volume. It is invisible on macOS, where bsdtar exits 0 on SIGPIPE,
  which is how it passed local testing.

  Now listed once into a variable and tested with herestrings. Not
  `printf | grep`, which takes SIGPIPE just the same and would have moved the
  bug rather than removed it. ([#347](https://github.com/rainmanjam/polyemesis/issues/347))

- **`hevc_vaapi` was offered and could not start.** `encoderProfiles` carried
  six of the twelve encoders the product lets you pick, so the other six fell
  through to an unknown-encoder branch. For most that meant missing tuning. For
  `hevc_vaapi` it was fatal: the branch left `prof.vaapi` false, so the command
  line got neither `-vaapi_device` nor `format=nv12,hwupload`, and VA-API
  encodes from GPU surfaces. It is never probed, so it was never greyed out
  either — selectable in the editor, dead at go-live.

  All twelve now carry a profile, and the values were read off real encoder
  option tables rather than inferred. **EXPERIMENTAL: an option table is not
  silicon.** `ffmpeg -h encoder=<name>` is answered from the binary and returns
  the same text on a machine with no such device in it, which is the machine
  every one of these twelve was read on — so no NVENC, QSV or VA-API encode has
  been observed running with the flags below. The `libx264`/`libx265` rows are
  exempt: that argv predates renditions. Nothing is disabled by the label; a
  flag that is wrong surfaces as an encoder that refuses to open, which the
  start gate already reports by name. That mattered: **`-profile:v high` is
  H.264 only, and an HEVC encoder does not ignore it, it refuses to open**
  (`x265 [error]: unknown profile <high>`). Copying the existing rows across —
  the obvious fix — would have broken every HEVC encode instead of fixing one.

- **Capped VBR did nothing on NVENC. EXPERIMENTAL: the fix has not been
  confirmed on an NVIDIA card.** The whole effect of this change is on an argv
  nobody has watched NVENC accept — the reasoning below is from FFmpeg's option
  tables and source, on a host with no GPU. `-rc cbr` was appended unconditionally,
  immediately before `-b:v/-maxrate/-bufsize`, so a rendition with a ceiling
  above its target still ran constant-bitrate and the operator's number was
  inert. The mode is now chosen from what was asked for, and emitted before the
  rest so an install that sets neither field gets a byte-identical command line.

  The other four families were checked and need no change, which is worth
  recording so nobody re-derives it: QSV has no `-rc` at all and infers the mode
  from `-b:v` against `-maxrate`, VA-API's `-rc_mode` already defaults to `auto`,
  and VideoToolbox is capped-VBR unless `-constant_bit_rate` is set.
  ([#341](https://github.com/rainmanjam/polyemesis/issues/341))

- **A failed release build could leave container tags with no release behind
  them.** The image and binary jobs ran in parallel, so a failure in the second
  left Docker Hub and GHCR already carrying `:latest` with no GitHub Release, no
  checksums and no SBOM. Because `install.sh` pins compose to `:latest`, that
  split the two install modes: docker operators would take the new version
  immediately while binary installs stayed put. The images now wait for the
  binaries.

- **Two pages stated the opposite of what the code does.**
  `docs/PLATFORMS.md` said `moderation:ban` was "deliberately not requested:
  nothing in polyemesis bans or times out a viewer" — while `internal/oauth`
  requests it and automod's action matrix carries both. That was not drift but a
  decision the page never caught up with: the scope was omitted on purpose, the
  omission was reversed when moderation shipped, and the code kept the original
  argument deliberately "because it is the argument to re-read if the decision
  is ever revisited". The page kept only the superseded half, which is the worst
  error available on that page — an operator reads it, connects an account, and
  is shown a permission the documentation promised would not be asked for.

  `docs/HARDWARE.md` said `libx264` stays the default on a machine with a
  working GPU and that "hardware is an opt-in you make deliberately".
  `DefaultVideoEncoder` returns the first encoder that **passed the probe**,
  hardware first. `docs/RENDITIONS.md` had it right all along, so the two pages
  contradicted each other with nothing to tell a reader which to believe.

- **`docs/UPGRADING.md` said nothing about the change that matters most to a
  restore.** No 0.7.0 section, and no mention of `secret.key` anywhere across
  the page — for the release that makes that file the difference between a
  restore and a silent outage. It also never mentioned `update.sh`, so operators
  were steered to the hand-rolled procedure rather than the guarded script
  `install.sh` writes. It now carries the 0.7.0 note, the one-way warning, the
  key check in *Rolling back*, and a `keyUnreadable` check in *Verifying* —
  because a restore that omits the key file looks completely successful until
  go-live.

- **`docs/ENCODING.md` overcounted the encoders twice in one sentence.** It
  claimed twelve hardware encoders "each probed with a real test encode": ten are
  hardware, and six are probed, with the HEVC verdicts inferred from their H.264
  siblings.

- **A stop arriving mid-spawn signalled nothing, and the child ran on
  unsignalled.** `Start()` publishes `p.running` and returns, but `p.cmd` is not
  set until `runOnce` has built the command, opened its pipes and had
  `cmd.Start()` return. A `Stop` landing between those two points took its
  `p.running` arm, cancelled the supervise context, and called `terminate()` —
  which found `p.cmd == nil` and returned having sent no signal and armed no
  escalator.

  `p.cmd == nil` meant two different things there, "no child yet" and "the child
  is already reaped", and nothing could tell them apart. `runOnce` uses
  `exec.Command` rather than `exec.CommandContext`, so the cancelled context
  killed nothing: the child was spawned behind a stop that had already given up
  on it, `cmd.Wait()` blocked on a process nobody had asked to leave, and `Stop`
  waited out its entire deadline before a `SIGKILL` it did not wait for. Found
  while investigating #126's one lead — a `teardown 12001.831ms STOP DEADLINE`
  that turned out not to be a slow child but a child nobody ever asked to
  leave. ([#126](https://github.com/rainmanjam/polyemesis/issues/126),
  [#330](https://github.com/rainmanjam/polyemesis/pull/330))

- **A binary or systemd install gets a guarded `update.sh` too, and both modes
  now check for `secret.key`.** `write_helper_scripts` opened with
  `[ "$MODE" = "docker" ] || return 0`, so the generated `update.sh` existed
  only for Docker. Docker operators got a script that refuses to upgrade when
  the backup would be a lie; systemd operators got the same procedure written in
  `UPGRADING.md`, where nothing checks whether they ran it or whether it worked.
  Migrations are forward-only in both modes, so the install that most needed the
  guard rail had the least.

  Neither mode checked for `secret.key`. Counting entries proves an archive is
  not empty; it does not prove it holds the one file whose absence cannot be
  recovered from. Now that destination stream keys are sealed at rest, a
  database restored **without** `secret.key` comes back with every destination
  disabled — correctly, but the restore reads as completely successful until
  someone goes live.

  Writing the test found a bug in the script itself: `cp -a src dest` **nests**
  when `dest` exists, so a second run inside the same minute produced
  `data.bak-<stamp>/data/` and then looked for `secret.key` in the wrong
  directory. It now refuses when the destination exists rather than guessing
  which of the two the operator meant — found by running the generated script
  twice, which is what a real operator does after a failed upgrade.
  ([#347](https://github.com/rainmanjam/polyemesis/issues/347))

- **The first-run screen used four colour tokens from the marketing site's
  palette, not the application's.** `border-border`, `bg-card-raised/40`,
  `bg-warn` and `bg-border` exist in `web/src/styles/global.css` and not in
  `ui/src/index.css`, so each resolved to nothing and the affected elements
  rendered unstyled. Caught by a UI test that was twice misread as flaky before
  it was believed.
  ([#352](https://github.com/rainmanjam/polyemesis/pull/352))

- **Cloudflare Workers appends header values rather than replacing them, so a
  `no-cache` default defeated every immutable asset rule.** Every fingerprinted
  bundle was being revalidated on each load despite carrying a correct
  `immutable` directive of its own.
  ([#334](https://github.com/rainmanjam/polyemesis/pull/334))

- **A playlist item whose file was refused after the fact said "not yet queued
  for normalisation" for ever.** The *Media re-check* above is the first thing
  in this product that can refuse a file which was already accepted, and the
  playlist had nowhere to put that answer: `GET /failover/playlist` reported
  the queue state it found, so an item whose source can never normalise showed
  the amber "working on it" reading, and the operator waited on a job that
  fails and re-queues on every save.

  An item with no derivative now names the refusal and its reason, and says
  that sending the same file again will not change it — the remedy for a
  refusal is a different file, not patience.

  **An item that is already on air keeps playing.** Its derivative was
  transcoded from those bytes and is intact, so pulling it off air would black
  out a running programme to report something the operator can act on whenever
  they like. It stays `ready`, and the finding arrives as a new `warning`
  field beside it. Readiness answers "may this go to air"; a re-inspection of
  the source is not an answer to that question once a derivative exists.
  ([#273](https://github.com/rainmanjam/polyemesis/issues/273))

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

- **Deleting a destination could end a broadcast this process never put on
  air.** The coordinator decided from the platform's answer alone; the phase it
  had actually confirmed was recorded and never read. They diverge in the case
  the code calls "the somebody-else's-broadcast story" — a broadcast started in
  YouTube Studio, or carried through an upgrade — where the platform says live
  and polyemesis never transitioned it. On YouTube `complete` is terminal.

- **Start and stop reported the wrong thing on a multi-source install.** The
  effect was read back from the DEFAULT engine only, so a destination belonging
  to another programme was simply not found: a failed enable reported as
  started, and a stop that left a process holding the port reported as clean.
  Invisible on a single-source install, which is every development box.

- **The debug bundle's size cap counted the wrong bytes.** It measured raw
  string length while JSON writes six bytes for a control character, so the
  ceiling the package states about itself was wrong by a factor of six —
  785,437 bytes against an asserted 393,216. Measurement, cutting and every
  budget deduction now run in encoded units.

- **Attributes logged inside a group reached the bundle as `{}`.** The key names
  survived and every value vanished, which reads as "the field was blank" rather
  than "the recorder cannot represent this".

- **YouTube's broadcast-create advice was dead on the real path.** It matched
  against a truncated body, and its own comment says the reason code sits past
  that cut — so an operator whose channel is not enabled for live streaming got
  the raw snippet instead of the sentence naming the button.

- **The secret key file was created with a Unix file mode alone.** The load path
  has always restricted it properly; the branch that MINTS the key did not, so
  on Windows the file it had just generated was readable by everyone.

- **A supervisor restart could leave a live child unreachable.** The teardown
  cleared the process handle unconditionally, so a predecessor still unwinding
  could blank a successor's — after which `terminate()` and `kill()` could not
  find the new child, and it kept running and kept publishing to a destination
  the operator believed was stopped.

- **Three published pages were missing from the sitemap.**
  `/multistream`, `/twitch-vod-track` and `/vs/streamlabs` were built and served
  but absent from the `lastmod` map.

- **A credential in an unrecognised attribute reached the debug bundle
  verbatim.** The recorder's scrub walk handled `string`, `[]string`,
  `map[string]any`, `error` and `fmt.Stringer`, and passed everything else
  through untouched — its comment asserted that what reached that branch
  "cannot carry a credential without having been formatted first".
  `slog.Any("detail", map[string]string{"token": key})` reaches it. Neither the
  declared secret set nor the residual `alerts.Redact` pass ever saw the value,
  and it travelled into a file intended for somebody who does not have the
  operator's server. Reproduced in four shapes — a map of strings, a slice of
  any, a struct, and a map of slices. Unrecognised values are now rendered and
  scrubbed before capture.

- **The debug ring counted its records and never weighed them.** The buffer
  bounded the record COUNT at 5,000 and nothing bounded their size, so a single
  enormous log line made the export unbounded — and the browser buffers that
  file whole, twice, in the tab of the person trying to report a fault. Each
  record now has an 8 KB budget, spent in a fixed order, applied AFTER the
  scrub so a cut can never strand the front half of a stream key as text the
  secret set no longer matches. The bundle states its own size and how many
  lines were shortened, and the confirmation dialog shows both before anything
  is sent.

- **Four of six pages were shipping with no cache revalidation.**
  `web/public/_headers` scoped `no-cache` to `/` and `/*.html`, and its comment
  claimed that covered the built pages. It did not: with
  `build.format: "file"` and `trailingSlash: "never"` a page is BUILT as
  `features.html` and SERVED as `/features`, and Cloudflare matches the request
  path. The failure that block exists to prevent — "a deploy is invisible until
  caches expire" — was live on `/features`, `/comparison`, `/docs` and
  `/download`. A build check now derives the expectation from the built output.

- **114 Copy buttons did nothing.** The click handler shipped inside
  `CodeBlock.astro`, which the rendered documentation pages never mount. Styled,
  focusable, labelled and inert.

- **Eleven copy defects**, found by three independent reviewers with different
  lenses. The ones that mattered were claims about other people's products:
  a sentence whose "either" retroactively negated the clause before it, turning
  a correct statement into the overclaim it was written to avoid; a wrong
  restream.io plan limit that contradicted a sibling page; four capability cells
  asserted "No" for competitors with nothing behind them; and a card presenting a
  DESCRIPTION of a function in the quoted-source treatment that gives its
  neighbours their authority.

  Also removed: a per-destination CPU figure that appears nowhere but in prose,
  and a claim that the second Twitch audio track works, which
  `docs/AUDIO-ROUTING.md` marks EXPERIMENTAL because no broadcast has ever been
  published through a key Enhanced Broadcasting minted.

- **An engine test asked for three contiguous UDP ports and checked one.**
  `testenv.FreeUDPPort` probes a port, releases it, and returns the number, so a
  three-port allocator built on it had two numbers nobody had checked and one
  that was already nobody's. It failed CI three times in one day on three
  different ranges, each time blaming the code under test for a port it never
  held. `testenv.FreeUDPWindow` reserves the window and holds it.

- **The installer offered to fix FFmpeg only for the hosts that did not need
  it.** `install.sh` has always known how to fetch a current static build and
  verify it carries libsrt before displacing anything — and offered that only
  when your FFmpeg was 6.x or 7.x, where it buys one feature. A host **below
  the 6.0 floor, or with no FFmpeg at all**, got an error and a reading list,
  one item of which was "a static build with libsrt" linking to the very
  releases page the script downloads from. It now offers. Declining is what
  ends the install, not the old version.

  The same gap was one branch over: an FFmpeg can clear 6.0 and carry **no
  libsrt** (Homebrew's does), which costs per-destination audio routing
  entirely, and that branch also told you to compile one yourself.

- **PATH order no longer decides which FFmpeg runs.** When the installer
  installs FFmpeg itself it pins the absolute path into the config it
  generates, so `/usr/bin` winning the PATH cannot silently give the service
  the older binary.

- **A release with no published `SHA256SUMS` is refused rather than installed.**
  A checksum *mismatch* already died; an **unverifiable** download warned and
  installed as root. `--allow-unverified` keeps the escape hatch as something
  you type.

- **YouTube's error advice never fired.** Both advice functions matched against
  the response body *truncated at 300 characters for display*, and a realistic
  YouTube 403 is 552 bytes with the machine-readable reason at index 448 —
  Google puts a long human message before `errors[]`. So the sentences written
  for "this channel is not enabled for live streaming" and for the concurrent
  and shared-ingestion quota limits could not be reached. The quota ones are
  what an operator hits at the moment they are trying to go live.

- **A taken port is offered a free one** during the interview, instead of the
  installer predicting a bind failure and then causing it.

- **An automod rule that asked for a timeout with no duration banned the viewer
  permanently.** Every adapter reads a zero duration as "forever" — that is the
  documented contract — and the executor passed the configured seconds straight
  through. A rule saved without `timeoutSeconds` therefore removed a viewer for
  good and logged it as a successful timeout. Saving such a rule is now refused,
  and the executor clamps at the point of use, which is what protects the rules
  already stored.

- **Retention deleted the subtitles of recordings that still existed.** The
  sweep identifies an orphaned transcript by walking the filename backwards and
  testing prefixes that end at a dot. Transcripts are written one file per track
  named after the speaker, joined with a *hyphen*, plus a merged `-all` file — so
  a transcript of a surviving recording never matched and was removed. The
  `.json` transcript does end at a dot, so it survived: the sweep kept the
  machine-readable file and deleted every human-readable subtitle track for a
  recording sitting in the library. Hyphens are now prefix boundaries too, which
  only ever keeps more files.

- **Cancelling a transcode could hang for ever.** The child is killed when the
  context ends, but a grandchild — x265 and SVT-AV1 both spawn their own workers
  — can hold the output pipes open after its parent is gone. The wait delay that
  exists for exactly this only takes effect inside the process wait, and the
  reader goroutines were waited on first, so the readers never saw end-of-file,
  the wait was never reached, and the delay never applied. Measured: the previous
  ordering was still blocked 25 seconds after cancellation; it now returns in
  about 300ms.

- **Every reconnect published a spurious destination-down webhook.** The
  ten-second dwell that absorbs a supervisor reconnect was declared, documented,
  and never applied: a zero dwell — which the configuration documents as "takes
  every default", and which is what the engine constructs — fell through the
  clamp for negatives without picking the default up. Any subscriber therefore
  received a down-and-up pair for every restart.

- **A library with more than 32,766 recordings rendered as if it were empty.**
  Four queries bind one parameter per recording from an unbounded list; past
  SQLite's limit the statement fails, and the handler substitutes an empty map.
  Every session then showed no members and no poster, every recording fell into
  the ungrouped list, and every title, description and tag disappeared — while
  the page still returned 200. Nothing had been lost and there was no way to
  tell that by looking. The queries are chunked, and the metadata failure is now
  logged like its two siblings.

- **Refiling several recordings at once left the session they came from
  reporting recordings it no longer had.** The bulk edit steals each recording
  from its previous session but only recalculated the destination. The
  one-at-a-time path already recalculated both, and said why.

- **The automod API key, model timeout and spend counters were stored but never
  read.** Setting the key returned "configured" without rebuilding the model
  checker, so it took effect only at the next restart and a rotated key kept
  sending the old one; the configured model timeout was dropped when the engine
  was built, so every model-decided timeout ran at the built-in default; and the
  statistics endpoint returned a hardcoded zero after the wiring it was waiting
  for had landed, which an operator watching their model bill reads as "nothing
  has been spent".

- **An internationalised hostname could never obtain a certificate.** The
  configured name was compared against the incoming SNI after lowercasing only,
  but a client sends the punycode form while the operator configures the
  readable one — two spellings of a single name, never equal. The policy refused
  to request a certificate for the only hostname it was configured with.

- **A Windows build could abort with `fatal error: found pointer to free object`.**
  The job-object teardown took the address of a local struct, converted it through
  `unsafe.Pointer` to `uintptr`, and handed that to `SetInformationJobObject`. A
  `uintptr` is an integer as far as the garbage collector is concerned, so nothing
  kept the struct alive across the call and the collector was free to reclaim it
  while the kernel still held the address. `runtime.KeepAlive` now bounds the
  lifetime at each of the three sites.

  **No vet had ever run for Windows**, which is why a class of defect the tool
  reports by name went unreported: every vet invocation in CI ran for the host
  platform, and `_windows.go` files do not compile there. `GOOS=windows go vet
  ./...` now runs on every code change, so the next one fails a check rather than
  a customer's stream.
  ([#440](https://github.com/rainmanjam/polyemesis/issues/440))

- **The generated systemd unit contained the output of `ps`.** The unit template
  is a heredoc, and one line used a backticked `ps` where it meant to name the
  command in prose. The shell ran it while writing the file, so the unit grew from
  40 lines to 215, 176 of them a process listing spliced into the middle of a
  service definition. `systemd-analyze verify` prints `Missing '='` for those
  lines **and still exits 0**, so the check that was supposed to catch this
  reported success; it is now the printed output that is asserted on, not the exit
  code.

- **Binary mode did not get the guard rails docker mode already had.** Its helper
  scripts were written in only one of the two branches that need them, and an
  interrupted install could run its rollback twice. There was also no uninstall
  script for binary installs at all — so the documented way to remove polyemesis
  worked for one installation method and silently did not exist for the other.

### Testing
- **The Windows uninstaller had never been parsed by anything.** No workflow
  referenced `deploy/windows/` at all, so a syntax error in a script that ends
  live broadcasts would have shipped undetected. It is now parsed and exercised
  on `windows-latest`, inside the existing required job rather than a new one,
  since branch protection names a fixed set of contexts and a new job would never
  gate a merge.

  The gate found a defect immediately: the live-broadcast check returned its
  results through a bare pipeline, and a pipeline that matches nothing emits
  nothing — so "no encoder is publishing" and "the process table could not be
  read" arrived as the same value. The caller treats the second as fatal, so on
  an idle host every uninstall refused with a message about the wrong thing, and
  the confirmation prompt was unreachable. It failed safe, but it did not work.
  (#509)
- **A test that asserted a state it could not reach, and quarantined itself
  instead of failing.** `TestASilenceTierLiftsTheHoldOnAnUnmeasuredLayout`
  exists to make the second term of `holdDests := !measured && silenceSig == ""`
  load-bearing — its own comment says deleting that term would otherwise pass
  every test in the file. Its setup asked for a silence tier on an unmeasured
  video-only probe with the slate off, and neither path to a non-empty
  `silenceSig` is open in that state: `wantSilence` returns "" unless `measured`,
  deliberately, and `holdSilence` only carries a frozen signature while the
  selector is off the primary. Verified by deleting the term, which passed.
  Rebuilt on the one state that does reach the branch — selector on the slate,
  signature frozen, layout unmeasured, which is an ingest that dropped out
  mid-event rather than one that never arrived — and it now asserts it got there
  before asserting anything else. `internal/engine` reports no quarantined
  tests. (#161, #486)
- **A supervisor warning failed an unrelated test, twice.** The second sweep in
  `TestEnsureFeedHolds...` asserted the log buffer was EMPTY, meaning to say the
  refusal was not repeated. `buf` is wired to `e.log`, the whole engine's
  logger, and the engine runs a source whose binary cannot start, so its
  supervisor retries on a backoff and logs "process exited" for the length of
  the test. Any retry landing between Reset and String failed it and then
  reported that WARN as a refusal storm. Narrowed to the refusal itself.
  (#474, #484)
- **flake-rate can run the Go mode under `-race`.** The Windows corruption in
  #440 is the only place the crash appears, and `ci.yml` runs `-race` on ubuntu
  alone, so no required job could see it. (#482)
- **Ten UI-drift guards, recovered from a branch that was never merged.** They
  check that the React frontend still offers what the Go side expects — the
  Facebook crosspost and donate controls, the destination dialog's save payload,
  the backup-ingest toggle, the card's link to a scheduled broadcast, the
  header's one question about being live, and the composer's tag and compliance
  pushes. `internal/db/limits_drift_test.go` and
  `internal/oauth/capabilities_drift_test.go` already established the pattern;
  these are eight more of it, plus two in `internal/oauth`.

  **They are here because each one was watched to fail, not because they
  passed.** All ten passed on recovery, which this release has learned means
  nothing on its own: a mutation was applied to the source each guard names, the
  test was run by its full name with `-v` so a mistyped filter could not report
  `[no tests to run]` as success, and the file was restored from a backup with
  `git diff` confirmed empty. One example of the class: deleting
  `id="dest-fb-donate"` kills the crosspost guard, and wrapping the block in
  `{false && …}` kills it too — which proves the guard bounds the block rather
  than grepping the whole file.

  **Two real defects were found in the recovered code and fixed before it
  landed.** A source-grep guard passes forever if the string it looks for
  survives in a comment: deleting the real code in `AppLayout.tsx` and leaving
  its text in a `// was: …` comment kept one test green.
  `facebook_ui_drift_test.go` already shipped `stripJSComments` for exactly that
  and documents it at length; the sibling file in the same package was not using
  it. And one guard sliced from an unguarded `strings.Index`, so a rename
  produced a slice-bounds panic instead of a readable failure.

  Recorded rather than fixed: the two `internal/oauth` guards remain
  comment-defeatable, because that package has no comment stripper and copying a
  forty-line helper between packages is the wrong repair. Promoting `readUI` and
  `stripJSComments` to a shared test helper is the follow-up.
  ([#367](https://github.com/rainmanjam/polyemesis/pull/367))

- **Five acceptance suites that talk to a real far end**, where before this
  release exactly one did. The argument for them is their hit rate rather than
  their theory: each of the first three found a defect on its first live run,
  and none was reachable by a unit test, because on each side of the boundary
  both halves were individually correct. What was wrong was the composition, and
  only a real far end refuses a bad composition.

  What they settled, and what it cost:

  | Suite | Needs a credential? | Found |
  |---|---|---|
  | `acceptance-chat.sh` | no | opens a real socket to each platform and asserts the specific refusal — DNS, TLS with the right SNI, and the line protocol, everything except a valid login ([#316](https://github.com/rainmanjam/polyemesis/pull/316)) |
  | `acceptance-oauth.sh` | 28 of 46 checks, no | every provider's OAuth surface is public, so discovery documents, advertised grant types and PKCE methods are all comparable against what `internal/oauth` hardcodes ([#322](https://github.com/rainmanjam/polyemesis/pull/322)) |
  | `acceptance-automod.sh` | no | the model endpoint's own key in `server.log` and in the operator's spend panel — `net/http` puts the request URL verbatim into `*url.Error`, and the redaction reasoning had been applied to the settings blob and nowhere else ([#324](https://github.com/rainmanjam/polyemesis/pull/324)) |
  | `acceptance-transcribe.sh` | no | three defects against the real model host ([#323](https://github.com/rainmanjam/polyemesis/pull/323)) |
  | `acceptance-hooks.sh` | no | needs a listener we control, not a platform account ([#325](https://github.com/rainmanjam/polyemesis/pull/325)) |

  The rule this yielded, recorded because it overturned the plan that preceded
  it: **ask what the far end will tell a stranger before assuming the test needs
  an account.** The note had assumed chat and OAuth had to wait for a connected
  account; between them forty-three checks run with no credential at all.

  Still open, and honestly so: nothing proves an OAuth refresh **succeeds**, and
  no chat suite performs a valid login. Both steps are written and skipping
  until an account is connected, so the hour-four token failure remains bounded
  from one side only.

- **A variable-frame-rate source keeps its timing through a rendition, and now
  fails the build if that changes.** `-fps_mode` and `-vsync` are deliberately
  never set, so a VFR source — screen capture, some phone encoders — passes
  through with its presentation timestamps intact. Measured on a fixture that is
  30 fps nominal and about 18 fps actual: 72 frames over 3.967 s in, 72 frames
  over 3.967 s out. Forcing CFR was measured too, and is worse: the same fixture
  becomes 120 frames, 48 of them duplicates filling gaps where the source had
  nothing to say.

  One measurement trap is recorded with it, because it produced a false alarm
  first: ffprobe reports a **uniform** `duration_time` for every frame of an
  MPEG-TS, because the container does not store per-frame durations and ffprobe
  derives them from `r_frame_rate`. Read that field and a VFR stream looks like
  it was silently resampled and lost a fifth of its running time. It was not.
  ([#342](https://github.com/rainmanjam/polyemesis/issues/342))

- **Three suites that existed and ran nowhere are now scheduled**, which is the
  gap this release keeps finding: a suite that is never executed is
  indistinguishable from one that was never written, except that it reads as
  covered.

  `acceptance-obs-multitrack.sh` had already established that OBS 30.2.3 sends
  no multitrack audio at all — a negative six documents now depend on — and
  getting it onto a runner surfaced two defects of its own. **The observer had
  no floor:** below FFmpeg 7.1 multitrack FLV does not demux, so on Ubuntu's
  stock 6.1.1 the suite reported one track whatever OBS sent. A green run on a
  host that could not have produced any other answer. It now proves the floor
  with a two-track round trip rather than a version string. It was also the last
  host suite not using the shared teardown, and running it twice back-to-back
  bound `:1935` while the previous run still held it — reporting "the RTMP
  listener never bound", a teardown bug wearing the costume of the product
  failure the suite exists to detect.

  `acceptance-transcribe.sh` checks twenty hardcoded claims about a model host
  nobody here operates — ten filenames to URLs and ten byte counts gating
  `VerifyModelFile`. Those can rot with no commit of ours, so they need a clock
  rather than a push.
  ([#355](https://github.com/rainmanjam/polyemesis/pull/355),
  [#356](https://github.com/rainmanjam/polyemesis/pull/356))

- **The rate-control test runs the whole path, because every individual piece
  already worked.** Asserting on `RenditionSpec` alone would have passed before
  #341 was fixed, so the test drives a stored row through the mapping and into
  the argv the encoder is actually started with.
  ([#354](https://github.com/rainmanjam/polyemesis/pull/354))

- **The upgrade guard that stops an install eating `secret.key` had no test**,
  and `acceptance-install.sh` turned out never to have run in CI at all.
  ([#353](https://github.com/rainmanjam/polyemesis/pull/353))

- **The partial-bind test reserved one half of a port and hoped for the other.**
  It now reserves udp4 first and retries, and uses `s.Report()` to distinguish a
  lost race from a real regression rather than reporting both as failure.
  ([#339](https://github.com/rainmanjam/polyemesis/pull/339))

- **A respawn gate that waited on a feed which had not started yet.**
  `StateStopped` meant both "never started" and "died", so the gate could not
  tell them apart — the same conflation that produced the mid-spawn stop above,
  on the respawn path instead of the teardown one.
  ([#290](https://github.com/rainmanjam/polyemesis/issues/290))

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

- **A required test reached a third party's live servers, and went red when they
  answered differently.** It constructed `Options{}` — the zero `oauth.Set`, which
  resolves to the platforms' real hosts — so it called `id.kick.com` and depended
  on Kick returning 401 *today*. Twice in one afternoon an S3-fronted edge answered
  `AccessDenied` in XML instead and the required matrix went red for a reason
  unrelated to the change under review. It now runs against an `httptest` stub.
  ([#439](https://github.com/rainmanjam/polyemesis/issues/439))

- **Every CI job now has a time ceiling, and the un-retried network installs are
  retried.** An audit rather than a sprinkle: 30 of 31 jobs already had
  `timeout-minutes`, and the one that did not was falling back to GitHub's default
  of six hours while waiting on a third-party scanner. Three network installs had
  no second attempt and now have three. Deliberately left alone, with the reasoning
  recorded so it is not redone: `npm ci` (npm already retries twice), the two
  `curl` sites that carry `--retry 3 --retry-all-errors` *and* a loop, and the
  installer's own `apt` — where apt is the subject under test and retrying it would
  hide the failure the job exists to catch.

  The gitleaks download needed more than a flag. It piped `curl` into `tar`, and
  `--retry` restarts a transfer from the beginning, so retrying would have re-fed
  bytes into a `tar` that had already consumed part of the stream — surfacing as a
  corrupt extract rather than the download failure it was. The archive now lands as
  a file first, which is what makes the retry safe.

- **The installer is now run, instead of only asked what it would do.** CI checked
  the installer's preflight and stopped there, so every defect above was reachable
  only by installing on a real machine. A job now installs, verifies the service,
  uninstalls, and checks the rollback path, on a runner rather than in a container
  because systemd has to be PID 1 for any of it to mean anything.

- **The acceptance teardown check could not fail, and could not attribute.** It ran
  as `trap 'poly_report_orphans' RETURN`, and a RETURN trap fires after the return
  value is already fixed — so the finding could never reach an exit status no matter
  what it found. It also counted every `ffmpeg` on the host, which cannot tell a
  suite's own leak from a developer's transcode or a sibling suite running in
  parallel, and a check that goes red for someone else's process is one people
  learn to ignore.

  Both are fixed, and fixing them immediately found that **the harness was
  manufacturing the leak it was later asked to detect**, twice over. `poly_stop_server`
  escalated to SIGKILL after 15s while polyemesis's own shutdown can spend 20s in
  `httpServer.Shutdown` alone before it reaches `eng.Stop` — the call that tears the
  destinations down — so the teardown killed the server mid-shutdown and orphaned its
  encoders; the grace is now read from the shipped unit's own `TimeoutStopSec`
  rather than chosen. And two suites SIGKILL polyemesis deliberately, to prove a
  crashed job is recovered and that the broker publishes its will message; on a real
  box systemd's cgroup reaps the children, and outside systemd nothing did, so those
  suites now reap what they knowingly orphan.
  ([#448](https://github.com/rainmanjam/polyemesis/issues/448))

- **A test helper could not find a free port when the kernel handed it one near
  the top of the range.** `FreeUDPWindow` probes an ephemeral port and scans
  *upward* for a run of free numbers, guarded by `start+n < 65535` — which is false
  on the first iteration when the probe lands within `n` of the ceiling. It then
  tried not a single bind and reported "no run of 3 free UDP ports" about a machine
  where everything below was free. Linux's default ephemeral range stops at 60999
  and never reaches it; macOS runs to 65535 and does, which is why it read as a
  macOS quirk rather than a bug. The scan now wraps.

- **A stop-path test was budgeted below the stop path's own worst case.** It gave
  `Stop` five seconds and asserted the error was nil, while this package's own
  constants put the permitted worst case at `shutdownGrace + drainGrace` = 10s. A
  loaded runner that merely delayed the child produced a deadline error with nothing
  wrong — the test failed at 5.01s, its entire budget, having asserted nothing about
  the code.

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
found. Every change below traces to a numbered finding in that audit.

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

[Unreleased]: https://github.com/rainmanjam/polyemesis/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/rainmanjam/polyemesis/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/rainmanjam/polyemesis/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/rainmanjam/polyemesis/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/rainmanjam/polyemesis/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/rainmanjam/polyemesis/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/rainmanjam/polyemesis/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/rainmanjam/polyemesis/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rainmanjam/polyemesis/releases/tag/v0.1.0
