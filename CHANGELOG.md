# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

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

[Unreleased]: https://github.com/rainmanjam/polyemesis/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/rainmanjam/polyemesis/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/rainmanjam/polyemesis/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/rainmanjam/polyemesis/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rainmanjam/polyemesis/releases/tag/v0.1.0
