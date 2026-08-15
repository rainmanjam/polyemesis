# Pre-tag sweep for v0.7.0

Five parallel read-only sweeps against `main` @ `f62fb74`, 2026-08-14, before
tagging v0.7.0. Every row marked **verified** was reproduced against the source
or by running something; **reported** means a sweep asserted it and it has not
been independently checked here.

All five sweeps have reported. The two external reviewers (codex and agy) both
returned the verdict **"do not tag"**, independently and for different reasons.

---

## The two that matter most

**B0a — GHSA-7jqx would be published claiming a fix that is partial.** The
advisory says *"The supervisor scrubs the `process exited` line."* True. But
`supervisor.go:703`, in the same function and on the same `msg` variable, logs
`"err", msg` **unscrubbed** on the give-up path:

```go
:669   p.log.Warn("process exited",     "err", p.scrub(msg), …)   // scrubbed
:703   p.log.Error("giving up on process", …, "err", msg)          // NOT scrubbed
```

`msg` is `runOnce`'s error carrying FFmpeg stderr, including the publish URL and
key. A refused destination with `MaxRestarts` set writes its key to `server.log`
after N retries — the exact leak the advisory claims closed. No test covers it
(`grep 'giving up' *_test.go` → nothing). Found by the sweep agent itself;
neither external reviewer caught it. **Verified.**

**B0b — Twitch Enhanced Broadcasting is a phantom feature.**
`grep -rn "polyemesis/internal/multitrack" --include='*.go'` returns **nothing**
outside the package's own tests. `multitrack.Negotiate` has no non-test caller.
`Destination.Multitrack` is persisted and **never read** by engine or API.

Yet: `DestinationDialog.tsx:1865` ships a toggle promising *"Asks Twitch at
go-live … and says so once"*; `features.astro:29` says it *"negotiates"*; five
docs describe it as working; and `WEBSITE-COPY-PROPOSAL.md:25` proposes public
marketing copy asserting it end to end. `CHANGELOG.md:282` is the only honest
statement in the tree — *"Nothing publishes through it yet."*

The generic two-mix egress **is** genuinely wired (`routing.CompilePair` →
`engine/destinations.go:310` → `ffmpeg.secondAudioMap`), so "two mixes to one
destination" is defensible. The *Twitch* framing is not. **Verified.**

---

## Blockers — do not tag until resolved

| # | Finding | Evidence | Status |
|---|---|---|---|
| B0a | Supervisor give-up path logs the stream key unscrubbed — see above | `internal/supervisor/supervisor.go:669` vs `:703` | verified |
| B0c | **The VOD second audio track pushes two tracks to a one-track ingest, silently.** The engine compiles the pair on `row.VODProfile != nil` alone and never consults `row.Multitrack`; `Validate()` mentions neither field. The field comment claims *"Nothing here enforces that pairing — **the engine reports it**"*. It does not. Combined with B0b (no negotiation ever happens), enabling the VOD mix on Twitch pushes two audio tracks to the ordinary RTMP ingest, which by this codebase's own documentation takes one. Twitch drops or rejects it and nothing warns. **This is the headline #141 feature failing on its primary platform.** | `internal/engine/destinations.go:90`; `internal/db/destinations.go:164-168` | verified |
| B0b | Enhanced Broadcasting inert; UI toggle and marketing claim it works — see above | `internal/multitrack` imported by nothing | verified |
| B10 | **`docs/UPGRADING.md` has no 0.7.0 section and no `secret.key` check.** Following it can disable every destination. `secrets.LoadOrCreate` *silently generates a fresh key* when the file is missing, so the server starts clean and the failure only shows at go-live. `install.sh:1038` names this gap in its own comment. Compounding it, **no markdown file anywhere mentions `update.sh`** — the guarded path is undocumented, so operators are steered to the unguarded hand-rolled procedure. | `docs/UPGRADING.md`; `internal/secrets/secrets.go:41-69` | reported |
| B11 | **`docs/PLATFORMS.md:348` states `moderation:ban` is "deliberately not requested". It is requested** — `internal/oauth/kick.go:102`. A false statement about what a consent screen will ask for. | `internal/oauth/kick.go:102` | reported |
| B12 | **`docs/HARDWARE.md:71` states the opposite of the code.** It says libx264 stays the default on a machine with a working GPU and "hardware is an opt-in". `Tools.DefaultVideoEncoder` returns the first *probe-passing* encoder from a preference list that puts hardware first. `RENDITIONS.md:295` has it right — internal contradiction. | `internal/ffmpeg/detect.go:406-434` | reported |
| B1 | **The `secret.key` upgrade guard falsely refuses legitimate upgrades.** `install.sh:1049` sets `pipefail`; `:1082` runs `tar tzf … \| grep -q "secret\.key"`. `grep -q` exits on first match, `tar` takes SIGPIPE, the pipeline returns 141, and `if !` inverts that into the error branch — so a backup that *does* contain the file is rejected. Fires or not depending on `readdir` order, i.e. inode order on ext4. Reproduced in `debian:bookworm-slim`: entry 1 of 4002 → false refusal, entry 4001 → pass. Invisible on macOS (bsdtar returns 0 on SIGPIPE) and `acceptance-install.sh` only exercises the binary-mode path. **Fix:** `listing="$(tar tzf "$f")"` once, then test the listing. | `scripts/install.sh:1049,1082` | verified |
| B2 | **`hevc_vaapi` is selectable and cannot start.** `encoderProfiles` contains only `h264_vaapi` with `vaapi:true`. `hevc_vaapi` takes the unknown branch, so `prof.vaapi` is false and the argv gets neither `-vaapi_device` nor `format=nv12,hwupload`. Never probed, so never greyed out; the start gate only refuses on *measured* failures. Same root cause leaves `libx265` without `-profile:v high -pix_fmt yuv420p`. | `internal/ffmpeg/rendition.go:204,241-257,467-471`; `internal/db/renditions.go:36` | verified |
| B3 | **Capped VBR — the flagship #341 feature — does not work on NVENC.** `EncoderNVENC.rateControl` is `{"-rc","cbr","-profile:v","high"}`, appended at `:296` immediately before `-b:v/-maxrate/-bufsize`. An `h264_nvenc` rendition with a ceiling above target still runs CBR. `ENCODING.md:68` claims capped VBR works, unqualified. | `internal/ffmpeg/rendition.go:188-192,296-301`; `docs/ENCODING.md:68` | verified |
| B4 | **GHSA-7jqx's version range is false.** Defect 5 (partially-masked minted Twitch key) was introduced by `c7db212` on 2026-08-13, four days *after* v0.6.0; `git tag --contains c7db212` is empty. So `< 0.7.0` overclaims and the advisory sentence *"The latest release, v0.6.0, carries all five defects"* is wrong — v0.6.0 carries four. | `git tag --contains c7db212` (empty) | verified |
| B5 | **GHSA-7jqx mis-describes defect 4.** It says an API response carried *a destination's stream key*. `281821c` shows it is the automod model provider's own third-party key (`?api_key=sk-…`) in `ModelStats.LastError`. No destination key involved. | `git show 281821c` | reported |
| B6 | **GHSA-7jqx claims two fixes the CHANGELOG never mentions** — the database/data-dir permission fix (#299/#300) and the `server.log` stream-key leak (#310/#311). An operator reading the notes is never told the database was world-readable, so is never prompted to assume prior exposure. The existing first Security bullet covers `process.log` (#306), a *different* defect. | CHANGELOG scan: `0644`, `0600`, `umask`, `#299`, `#310` all absent | reported |
| B7 | **Two false claims about a competitor.** `comparison.astro:38` says Restreamer has REST-only metrics — its README advertises Prometheus, and it exposes GraphQL. `comparison.astro:53` claims no per-destination loudness — it has `FilterSelect` per publication with `Loudnorm.js` among the filters. Both rows are mirrored in `docs/COMPARISON.md` and a build check enforces agreement, so each needs changing in two places. | `web/src/pages/comparison.astro:38,53`; upstream sources | reported |
| B8 | **`ENCODING.md:51` — "Twelve hardware encoders … each probed with a real test encode."** Ten are hardware (12 constants include libx264/libx265); only **six** are probed (`probeCandidates` = 5 hardware + x264). HEVC verdicts are inferred from the H.264 sibling. Restated wrongly at `RENDITIONS.md:259`. | `internal/ffmpeg/probe_encoders.go:82`; `internal/ffmpeg/rendition.go:26`; `internal/db/renditions.go:23-39` | verified |
| B9 | **`CONTRIBUTING.md:118` claims PR coverage a docs-only PR does not get.** Says "eleven host acceptance suites"; there are twelve, and since #351/#357 a documentation-only PR runs **zero** of them — the matrix reports green having executed nothing. Documented in none of the four testing docs. | `.github/workflows/ci.yml:1205-1225,1245` | verified |

---

## Should fix before tagging

| # | Finding | Evidence | Status |
|---|---|---|---|
| S1 | **A failed `binaries` job leaves a half-published release.** `release.yml` has no `needs:` anywhere — `images` and `binaries` run in parallel. If `binaries` fails, Docker Hub and GHCR already carry `:0.7.0`/`:latest` with no GitHub Release. Since `install.sh:729` pins compose to `:latest`, docker operators upgrade immediately while binary installs stay on 0.6.0. **Fix:** `needs: binaries` on `images`. | `grep -n 'needs:' .github/workflows/release.yml` → no output | verified |
| S2 | **Existing 0.6.0 installs never receive the guard.** `update.sh` is generated at install time and written to disk, so a 0.6.0 operator runs the *old, unguarded* script for the 0.6.0 → 0.7.0 upgrade. `git show v0.6.0:scripts/install.sh` has no `secret.key` check at all. Release notes must tell them to re-run `install.sh` first. | `git show v0.6.0:scripts/install.sh` | reported |
| S3 | **`docs/UPGRADING.md` says nothing about key sealing.** `grep -in 'seal\|encrypt\|secret\.key'` returns nothing across 196 lines. Line 23 still teaches the weaker check the generated script outgrew; "Rolling back" never mentions the key file; there is no 0.7.0 entry — despite this being the most consequential change for restore safety in the release. | `docs/UPGRADING.md` | reported |
| S4 | **Enhanced Broadcasting ships without its GPU precondition** on the home page and in the comparison table, where the cell reads a bare "Yes". `negotiate.go:128` refuses outright when `len(a.Hardware.GPU) == 0`. The project's own `COPY-CONSTRAINTS.md:50` requires stating it "in the same block that announces the feature, not in a footnote," because the audience self-hosts on VPSes. `/features` gets it right. | `web/src/pages/index.astro:204`, `comparison.astro:58`; `internal/multitrack/negotiate.go:128` | reported |
| S5 | **All three advisories are `severity: low` with null CVSS.** GHSA-7jqx covers at-rest disclosure of every destination stream key, the admin bcrypt hash and session secrets in a world-readable file — and proves it with `sudo -u nobody` succeeding. Its workaround also under-scopes rotation: "rotate every key that has ever been refused" is right for defect 2, but defect 1 exposed *all* of them. | GHSA API | reported |
| S6 | **GHSA-wv59's only workaround assumes a reverse proxy.** polyemesis terminates TLS itself in a supported mode; those operators have no remedy offered. | GHSA API | reported |
| S7 | **`ENCODING.md:30` overclaims the GPU requirement.** The "Required for" cell reads as universal, but a second audio track needs no GPU on any non-Twitch target. Knock-on: `ENCODING.md:20-22` and `RENDITIONS.md:51` ("audio encoded once, never twice") are false for a destination with a VOD track. | `internal/db/destinations.go:150-170`; `internal/routing/pair.go:18-56` | reported |
| S8 | **Test-doc counts are broadly stale.** 23 acceptance suites exist, `live-test-coverage-gaps.md:11` and `TESTING.md:443` say 17. `TEST-STRATEGY.md:95` says OAuth has "no integration test at all" (46 checks exist); `:94` says four chat adapters never tested against a real server (five, and `acceptance-chat.sh` hits real hosts); `:20` records browser E2E as absent, contradicting `:62` of the same file. `CONTRIBUTING.md:42` states a Go floor of 1.26.5 against `go.mod`'s 1.26.6, which hard-fails. | as cited | verified |
| S9 | **`features.astro:29` illustrates the flagship feature with a screenshot that does not contain it**, and its `alt` text is copy-pasted from the section above. | `web/src/pages/features.astro:29-35` | reported |
| S10 | **`features.astro:95` caption says "four platforms" under a heading saying five.** Rumble is the fifth. | `web/src/pages/features.astro:89,95` | reported |
| S11 | **`index.astro:42` misattributes a limit to OBS** — "up to 32 OBS split audio tracks". 32 is our ceiling (`MaxTracks`); OBS offers 6, which the page's own H1 says. | `web/src/pages/index.astro:42`; `internal/routing/profile.go:27` | reported |
| S12 | **Footer version has no `--match`.** `git describe --tags --abbrev=0` returns v0.6.0 today and could pick up `backup-pre-github` or `rescue-main-*`. Also `pages.yml` checks out at `fetch-depth: 1` (no tags), so the footer is permanently blank, and tagging does not redeploy the site. | `web/src/components/Footer.astro:15`; `.github/workflows/pages.yml:70` | reported |

---

## From the external reviewers, verified against source

| # | Finding | Severity | Raised by |
|---|---|---|---|
| X1 | **Advisory #1's "cleartext at rest" wording is broader than the fix.** `sources.token`/`prev_token`, the ingest JSON (SRT passphrases, legacy RTMP keys, pull URL credentials) and the whole Settings JSON are still plaintext — deliberately, per `schema.sql:61-67`. But `schema.sql:67`'s comment *"stream_key on destinations is stored the same way"* is now **false**. Either scope the advisory to destination keys or disclose the residual. | SHOULD-FIX | codex |
| X2 | **Plaintext v0.6 keys survive an upgrade in freed pages and WAL frames.** The sealing migration only `UPDATE`s rows; there is no checkpoint or `VACUUM` (zero hits in `internal/db/*.go`). Mitigated by the 0600 fix, but real for anyone restoring a backup taken across the upgrade. | SHOULD-FIX | codex |
| X3 | **Key-in-URL bypasses the sealed column.** A custom RTMP destination with the key embedded in `URL` and `StreamKey` empty is stored plaintext. | SHOULD-FIX | codex |
| X4 | **Tests overclaim.** `destination_secrets_test.go` asserts logical SQL values, never raw db/`-wal` bytes — so it could not have caught X2. `mqtt_broker_disclosure_test.go:112` calls `Validate()` while claiming "real sink, end to end"; it never observes an actual HTTP 400 body. | NICE-TO-HAVE | codex |
| X5 | `internal/multitrack/live_test.go:36` passes **vacuously** when Twitch is unreachable — `unreachable()` returns rather than skipping, and `POLYEMESIS_REQUIRE_NET` is set in no workflow. Loudly logged, so weaker than framed, but it proves nothing in default CI. | NICE-TO-HAVE | agy |
| X6 | `MigratePlatformAccountScopeVer` error path skips `sqldb.Close()` and error wrapping; all seven sibling migrations do both. The comment above it also describes the wrong migration. | NICE-TO-HAVE | agy |

**Refuted, and worth recording so it is not re-raised:** agy claimed `update.sh`
locks out v0.6.0 installs lacking `secret.key`. Wrong premise — v0.6.0's
`main.go:205` calls `secrets.LoadOrCreate` unconditionally at boot, so every
v0.6.0 install that ever started has the file.

**Not verified, do not treat as vetted:** agy's ggmlMagic and mocked-transport
items describe defects *this diff already fixed* (historical narrative, not open
issues); its `hlsHandler` auth-wrapping claim was not traced. codex's line
numbers were approximate (the code was confirmed at nearby lines).

## From the Go sweep

| # | Finding | Severity |
|---|---|---|
| G1 | **Rolling back to v0.6.0 silently blanks every stream key.** Verified empirically against real DBs seeded from each tag: the forward upgrade is clean and idempotent, but the v0.6.0 binary against an upgraded DB yields `streamKey="" enabled=true` for every destination — the backfill blanks the plaintext column and v0.6.0 has no concept of `stream_key_enc`. There is no `PRAGMA user_version` for a downgrade guard to read. **Needs a release note that the upgrade is one-way once a key file exists.** (The agent also tested whether the blanked plaintext survives as freelist residue — it does not. Hypothesis disproved, recorded so it is not re-raised.) | SHOULD-FIX |
| G2 | **14 media tests bypass the repo's own anti-skip guard.** `testenv.FFmpegBinary` + `POLYEMESIS_REQUIRE_FFMPEG` exist precisely so a missing ffmpeg *fails* rather than skips (#187). Tests added this cycle call `exec.LookPath` + `t.Skip` directly. Verified by stripping ffmpeg from PATH with the guard set: **41 tests fail (guard working), 49 skip while their packages print `ok`**. If the ffmpeg install step breaks, audio-copy bit-exactness, VFR timing and multitrack ordering regressions all go green. `routing/pair_test.go:309` does it correctly — inconsistency, not ignorance. | SHOULD-FIX |
| G3 | **`AutomodModel.TimeoutForBan` is settable and never read** — declared live-reloadable, exposed in the UI, but `ApplyAutomod` maps 8 of 9 fields and skips it. Set `timeoutForBan: 600`; chatters are silenced for 300s while the settings page shows 600. Another #341-class defect; predates v0.6.0. | SHOULD-FIX |
| G4 | `destSecrets(row, extra ...string)` documents `extra` as "the Twitch Enhanced Broadcasting minted key". **All three non-test call sites pass no `extra`.** `TestTheMintedKeyIsMaskedWholeAndNotJustItsTail` passes — pinning redaction of a credential production never mints. Corroborates B0b: the seam was built and never connected. | NICE-TO-HAVE |
| G5 | `NewSecretSet` logs refusals at `log.Debug` while its own doc insists *"Refusals are LOGGED, never silently dropped."* At the default Info level they are silently dropped. | NICE-TO-HAVE |
| G6 | `internal/api/upgrade.go:420` builds the artefact filename from the feed's `tag_name` unsanitised while `:465` carefully escapes the same variable; `tag='../../../../etc'` → `filepath.Dir` = `/`, which `:285` `RemoveAll`s. Requires a compromised `api.github.com` *and* matching `SHA256SUMS` over TLS, so it does not widen blast radius — but it is a one-line `filepath.Base()` fix. | NICE-TO-HAVE |
| G7 | `internal/api/media.go:74` — an oversized upload returns 500 instead of 413. `internal/web/web.go:132` — `stat, _ :=` then `stat.ModTime()` nil-derefs; unreachable with `embed.FS`, but `HandlerFor` is exported and takes an arbitrary `fs.FS`. | NICE-TO-HAVE |

**Verified clean by the Go sweep, with the work actually run:** full suite passes,
`go build`/`go vet` clean, `go test -race` green across ten packages including
supervisor ×3. No panics from user input, no goroutine or resource leaks in the
diff, migrations forward-only and idempotent with no data-loss operation, no
vacuous `-run` filters or excluded packages. It also **refuted** a raised
finding — an "unguarded state mutation" at `supervisor.go:918` does not hold;
the fields are under `p.cmdMu`/`p.mu` and the race detector is clean.

> **The sweeps disagree on one point, and it matters.** The Go sweep marked
> advisory (a) **FIXED** after checking the sealing, the backfill and the
> `readSafe*` paths. The external-review sweep found `supervisor.go:703`, and I
> confirmed it myself. Both are right about what they looked at; (a) is fixed at
> rest and in the `process exited` line, and still leaks on the give-up line.
> A "verified fixed" from one angle is not a clearance.

## More docs findings

| # | Finding |
|---|---|
| D1 | `docs/CONFIGURATION.md:148` says of environment variables *"There are none."* `RUMBLE_CHAT_API_KEY` and `RUMBLE_CHAT_CHANNEL` exist, and `PLATFORMS.md:181` says the key goes there "and nowhere else". |
| D2 | **`secret.key` described as OAuth-only in five places** — `SECURITY.md:120`, `CONFIGURATION.md:133`, `FAQ.md:140`, `INSTALL.md:298`, `PLATFORMS.md:360`. All now incomplete, and none states the disable consequence. |
| D3 | **Go floor wrong in five docs** — `go.mod` is 1.26.6 (a hard floor since Go 1.21); README, CONTRIBUTING, MODULES and INSTALL all say 1.26.5+. |
| D4 | `docs/COMPARISON.md:96` — *"Every destination decodes and re-encodes its audio, always."* False: `AudioEncoding.Copy` stream-copies, refused only on RTMP and Icecast, so SRT and file destinations can copy. **Understates the product in its own honest-loss section.** |
| D5 | `docs/API.md` documents none of 0.7.0's new surface — `vodProfile`, `keyUnreadable` (a field an operator hits after a bad restore), `maxrateKbps`/`bufsizeKbps`. `TROUBLESHOOTING.md` has no entry for a destination disabled by an unreadable key. |
| D6 | `docs/ARCHITECTURE.md:590` names four `internal/ffmpeg` files that do not exist; omits `internal/multitrack/` and `routing/pair.go`. |
| D7 | `docs/AUDIO-ROUTING.md:60` — `vodProfile` has "its own sample rate". False: `-ar` is unqualified and binds both streams from the primary's rate; `-b:a` is shared too. |
| D8 | `docs/MODULES.md:170` says `Dockerfile.vaapi` is `ubuntu:24.04`; it is `26.04`. `HARDWARE.md:114` has it right. |
| D9 | `PLATFORMS.md:251` omits three Twitch scopes actually requested; `:55` says "the other twenty-five entries" against 33 presets / 19 non-matrix. |
| D10 | `CHANGES-SINCE-v0.6.0.md:3` says 132 commits; actual is 148. Dependency versions stale in both inventory docs. `WEBSITE-COPY-PROPOSAL.md:4` cites `copy-constraints.md` — resolves on macOS, breaks on Linux. |

## Nice to have

| # | Finding |
|---|---|
| N1 | `go install …@v0.7.0` compiles but ships a **blank dashboard** — `.gitignore:12` excludes `internal/web/dist/*`. Not advertised anywhere, but the tag makes it fetchable via the module proxy. |
| N2 | Missing CWEs: GHSA-wv59 → CWE-548; GHSA-jqc3 → CWE-532 + CWE-209. |
| N3 | A botched merge severed a doc comment in the security-critical sealing path — `go doc ./internal/db Warning` renders "sealStreamKey splits a stream key into the pair of columns that store it: Warning is one advisory finding…". `internal/db/destinations.go:738,781`. |
| N4 | v0.4.0–v0.6.0 are annotated, unsigned tags. Use `git tag -a v0.7.0 -m "…"` to match. |
| N5 | Undocumented: the `fps=` filter inserted before scaling (+17% measured on 4K60→1080p30); bufsize upper bound of 400 000 kbps; dimension bounds 128–7680 (even only); FPS ≤ 240. |
| N6 | `comparison.astro:75` obs-multi-rtmp "around 4,975 stars" — actual 4,992. `:46` restream.io "2–8 by plan" is true for named tiers but Enterprise is custom. |
| N7 | `AuthScreen.tsx:173` says destinations "will not open", which reads as transient; the code's word is **disabled**, a state the operator must fix. |
| N8 | `index.astro:332` names Castr as an alternative and links to `/comparison`, which never mentions it. |
| N9 | `features.astro:258` says "All eight capabilities" against a table rendering nine rows. |

---

## Verified clean — recorded so nobody re-derives it

- **SBOM guard**: 9/9, and fails correctly on each individual ecosystem loss.
- **Version embedding**: built with real release ldflags, `-version` prints `polyemesis v0.7.0`; windows/arm64 cross-compiles clean.
- **All four release secrets present**; checksums resolve via `filepath.Base`.
- **0.6.0 → 0.7.0 needs no flag day**: `openStreamKey` prefers ciphertext and falls back to the plaintext column, so pre-upgrade rows work from the first read.
- **Binary-mode update guard** verified against four staged directories: empty → refused; db without `secret.key` → refused; healthy → pass; pre-existing backup dir → refused.
- **No stale `0.6.0`** to bump outside test fixtures and historical docs.
- **VFR**: `-fps_mode`/`-vsync` appear in no non-test code; the regression test asserts frame count and PTS span.
- **Docker Hub description sync** covered twice; tag/prerelease logic consistent across both jobs.
- **~15 other security entries** in the 0.7.0 notes were each checked against the tags: every one fixes a never-released regression, so none needs its own advisory.
- **Marketing site links**: all internal anchors resolve; all 17 linked `docs/*.md` exist; all 7 screenshots present and none orphaned.
- **Competitor claims other than B7**: verified true against primary sources, including exact quoted source lines from obs-multi-rtmp, Aitum and MistServer.
