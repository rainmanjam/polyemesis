# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

### Added

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
  a function nobody wrote. The distribution: 87 live, 49 respawn, 1 rebind, 2
  on-demand, 2 next-start.

### Fixed

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
- **The macOS SRT wildcard failure is documented where it is met.** The startup
  warning fires at boot; operators meet the failure when they point an encoder
  at the box. `docs/TROUBLESHOOTING.md` now carries the measured matrix under
  the symptom, because the existing checklist sent readers to the firewall,
  which the hosted-runner results rule out. See issue #28.

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

[Unreleased]: https://github.com/rainmanjam/polyemesis/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/rainmanjam/polyemesis/releases/tag/v0.1.0
