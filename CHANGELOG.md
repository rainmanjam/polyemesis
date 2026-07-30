# Changelog

All notable changes to polyemesis are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project intends to follow [Semantic Versioning](https://semver.org/) from
its first tagged release.

## [Unreleased]

Nothing has been tagged yet. Everything below is on `main` and is what a first
release will contain. Entries are grouped by what they do for an operator rather
than by which package changed.

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
  pages. Platform status now uses one vocabulary — **Primary**, **Verified**,
  **Unproven** — defined once and used identically everywhere.

[Unreleased]: https://github.com/rainmanjam/polyemesis/commits/main
