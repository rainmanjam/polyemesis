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

### Operations

- Prometheus metrics, alert rules with webhook delivery, and schedules.
- TLS with automatic certificate selection; HSTS deliberately opt-in and never
  sent over a self-signed certificate.
- API tokens, hashed at rest and individually revocable.
- Fourteen UI languages.

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

[Unreleased]: https://github.com/rainmanjam/polyemesis/commits/main
