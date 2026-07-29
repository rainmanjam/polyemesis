# Roadmap batch — progress and hand-off

**Last updated:** 2026-07-29, after destination-settings Part B (PR #21).
**Task:** build roadmap items **0, 1, 2, 3, 6**. Delivery: **one PR per item**,
stacked. Read `docs/roadmap/README.md` for the sequence and each item's design.

Everything needed to resume is here. Designs are already written and verified —
**do not re-derive them**, read the roadmap doc for the item.

---

## Decisions already made (do not re-ask)

| Question | Answer |
|---|---|
| Overlays scope | **v0.5, image watermark only.** No text, no clock, no dynamic feed, no preview endpoint, no font embedding, no `detect.go` filter probe |
| MQTT dependency | **Approved:** `github.com/eclipse/paho.golang` via `autopaho`. Exactly 1 net-new module |
| Delivery | **PR per item**, stacked on the previous branch |
| Items 6 & 8 (overlays, WebRTC) | Were deferred, then overlays was put back in scope. **WebRTC stays deferred** |

## Branch stack

```
main
 └─ docs/restructure            PR #11   docs restructure + roadmap
     └─ feat/rendition-ui-controls   PR #12   item 0   DONE
         └─ feat/playlist-phase-0    PR #13   item 1   DONE
             └─ feat/hls-latency         PR #14   item 2   DONE
                 └─ feat/destination-settings PR #15  doc + Kick key  DONE
                     └─ feat/mqtt-telemetry      PR #16   item 3   DONE
                         └─ feat/overlays-v05        PR #17   item 6   DONE
```

Each branch bases on the one above it. **Do not branch from `docs/restructure`
directly** — the roadmap `README.md` status column is edited by every item's PR
and will conflict.

## Status

- [x] **Item 0** — rendition UI controls (`aspectMode` / `padColor` /
      `deinterlace`), field-presence drift guard, deinterlace validation. **PR #12**
- [x] **Item 1** — `acceptance-playlist-phase0.sh` (15 checks) +
      `docs/SCHEDULED-BROADCAST.md`. **PR #13**
- [x] **Item 2** — LL-HLS: fps-independent keyframes (a real bug fix), 1s
      segments, derived list size, `program_date_time`,
      `maxLiveSyncPlaybackRate`. **PR #14**
- [x] **Item 3** — MQTT retained telemetry, HA discovery, 20-check acceptance
      suite against real mosquitto. **PR #16**
- [x] **Item 6** — Overlays v0.5, image watermark only. **PR #17**
- [ ] **NEW: destination settings** — `docs/roadmap/DESTINATION-SETTINGS.md`.
      Branch `feat/destination-settings`, stacked on `feat/hls-latency`.
      Research workflow `wf_de7a8e72-9b4` grounds the platform metadata fields,
      audits Go SDKs, and adversarially checks the YouTube multitrack claim.
      **Do not write the metadata section until that returns** — the whole point
      is that it is grounded rather than recalled.

      Sections A (transport/muxer), B (audio encoding) and C (resilience) do NOT
      depend on the research and can be built first.

      **RESEARCH COMPLETE (workflow wf_de7a8e72-9b4). Doc written and committed
      as `docs/roadmap/DESTINATION-SETTINGS.md`. Two headline results:**

      1. **YouTube multitrack live audio is REFUTED — do not build it.**
         Refuted on all four mechanisms: `multipleAudioStreams` is a documented
         ingest-validator error ("must only contain one audio stream"); HLS says
         "only single-track audio is supported" twice; `boundStreamId` is a
         singular scalar and `bind()` replaces rather than accumulates; the
         multi-language feature is upload/Studio-only. TRAP: YouTube documents
         5.1 surround for AAC over RTMP — that is ONE stream with six CHANNELS
         and there is no per-channel language selector, so it yields a garbled
         surround mix, not selectable languages.

      2. **Kick's stream key IS retrievable and PLATFORMS.md is wrong in three
         places.** It is `stream.key` on `GET /public/v1/channels`, behind a
         `streamkey:read` scope polyemesis does not request. `channel:read` does
         NOT cover it. There is no dedicated stream-key endpoint, which is why
         an endpoint-by-endpoint reading missed it. **THIS IS THE NEXT TASK** —
         half a day, removes a documented limitation.

      **SDKs: hand-roll everything.** youtube/v3 = 20 net-new modules incl. gRPC,
      protobuf, 5x OpenTelemetry, go-logr/stdr; not escapable; +11.9 MB real.
      helix=2, huandu/facebook=1, kick-sdk=1 — rejected on SURFACE not cost.
      Caveat carried: the audit's binary-size numbers were inflated ~40-50x by
      measuring against an fmt-only hello world; and the "old path" quote in
      DEPENDENCIES.md means abandoned dgrijalva/jwt-go, NOT golang-jwt/jwt/v4.
      `golang.org/x/oauth2` is the one genuinely open question (TokenSource
      refresh/cache races in hand-rolled code were never audited).

      **FFmpeg 8.1.2 probe results — two corrections to the first draft of the
      settings table, both found by running `ffmpeg -h`:**

      - `-rtmp_live` is flagged `.D.........` — **demuxer/input only.** It is NOT
        an output option and cannot be set on a destination. The first draft
        listed it as a missing setting. Wrong; drop it.
      - `-max_muxing_queue_size` exists, but its help text is narrower than the
        rationale given: *"maximum number of packets that can be buffered while
        waiting for all streams to initialize"*. It is about stream INIT, not
        ongoing audio/video interleave divergence. The related knob for the
        steady state is `-muxing_queue_data_threshold`. Re-justify or drop.
      - `-flvflags no_duration_filesize` IS `E..........` (encoder side) and is
        genuinely available.

      Lesson worth keeping: the settings table was written from recollection of
      FFmpeg, and roughly a third of the transport section did not survive a
      two-minute `ffmpeg -h` check. Probe every flag before designing around it.

      The load-bearing open question: does YouTube accept multiple audio tracks
      on ONE live ingest, or is multi-language audio VOD-only / separate-ingest?
      If VOD-only the idea dies; if separate-ingest it is buildable but is N
      ingests, not N tracks, and the design changes completely.

---

## Item 2 — LL-HLS (DONE, PR #14)

Landed as designed. Two things worth carrying forward:
- The frame-rate change was a **bug fix**, not tuning: `-g SegmentSeconds*30`
  emitted `EXTINF:1.200000` for a requested 1s at 25 fps. Proven with
  `TestPreviewSegmentsAreTheLengthTheyClaim`, which runs real FFmpeg at 25 and
  30 fps. The old form misses by 5x the tolerance.
- `lowLatencyMode` was already set and inert, but for a subtler reason than
  either the design or its reviewer gave: **`maxLiveSyncPlaybackRate` defaults
  to 1**, so the guard short-circuits regardless. Setting it to 1.08 is the
  actual fix.

## Item 2 — original notes

Design: `docs/roadmap/LL-HLS.md`. **Zero new dependencies.** Target:
preview latency 4.2–6.2 s → 2.2–3.2 s.

**Settled fact, verified against the pinned binary — do not re-litigate:**
FFmpeg 8.1.2's `hls` muxer cannot emit `EXT-X-PART`. No `hls_part_time`, no
`hls_server_control`. Its `dash` muxer *does* ship `-ldash`, so this is a scope
decision, not a version lag. **Option (a), tune what exists, is the chosen path.**

The six changes, in `internal/ffmpeg/build.go` around line 790:

1. `-g <n*30> -keyint_min <n>` → `-force_key_frames expr:gte(t,n_forced*<n>)`,
   keep `-sc_threshold 0`. **This is a bug fix**: the GOP is hardcoded to 30 fps,
   so a 25 fps ingest overshoots every segment by 20%.
2. `SegmentSeconds` default 2 → 1. Validation range stays 1–10.
3. `-hls_list_size` becomes derived: `max(6, ceil(8/SegmentSeconds))`. Add
   `-hls_delete_threshold 2`.
4. Add `+program_date_time` to `hls_flags` — this is what makes latency
   *measurable* rather than claimed.
5. **Stay on MPEG-TS.** fMP4 buys zero latency and costs an `init.mp4` lifecycle.
6. `ui/` hls.js: `lowLatencyMode: true`, `maxLiveSyncPlaybackRate: 1.08`,
   `liveSyncDurationCount: 2`, `liveMaxLatencyDurationCount: 6`.

**Corrections already folded into the doc — keep them, they were refutations:**
- `lowLatencyMode` is **not** inert (`hls.js:33592` gates the playback-rate
  catch-up controller on it). Do not write a comment saying it is.
- `TARGETDURATION` does **not** inflate — FFmpeg rounds. Change 1 is still right,
  but for segment accuracy, not amplified latency.
- The list-size hazard is **scale-invariant**; bumping to 8 is margin, not a fix.
- Mean saving is **≈2.5 s, not 3.0 s**. Any gate must use 2.5.

Migration: **do not rewrite an operator's stored `SegmentSeconds`.** Change the
default for new installs only.

## DONE — Kick stream key (PR #15)

Shipped. `streamkey:read` added to Scopes(); `stream.key`/`stream.url` added to
kickChannel; Ingest reads them; ManualKeyReason repurposed as reconnect advice;
capability matrix Kick streamKey manual -> yes; PLATFORMS.md corrected in all
three places. 4 tests changed (they encoded the old belief), 3 added (positive
cases). **Ingest deliberately does NOT default a missing URL to a hardcoded
host** — a first draft did and it was removed; a guard test covers it.

## DONE — item 3, MQTT telemetry (PR #16)

Shipped: `internal/mqtt` (slug/topics/state/client/telemetry/discovery),
`db.MQTTSettings` + sealed `mqtt_creds` row, `cmd/polyemesis/mqtt.go` runner,
`docs/MQTT.md`, `scripts/acceptance-mqtt.sh` (20 checks, mosquitto in Docker).
Also closed the `engine.Status` source-name gap and fixed a fifth stale Kick
test that PR #15 missed.

**Three design corrections, all recorded in docs/roadmap/MQTT.md:**
1. `Queue: nil` IS substituted with memory.New() (auto.go:271, true in v0.23.0)
   but the substitution is **INERT** — the queue is read only by
   PublishViaQueue; ConnectionManager.Publish bypasses it and returns
   ConnectionDownError. No no-op queue needed. The design's conclusion was wrong.
2. 4 hex is too few (68% birthday collision across 300 names). Shipped 8 hex.
3. A name already shaped like `x-1a2b3c4d` aliases with any name hashing to
   that. Closed by hashing anything matching the suffix pattern.

Measured: exactly 1 net-new module, **+586 KB on 25.4 MB (+2.4%)** paired build.

## DONE — item 6, Overlays v0.5 (PR #17)

`internal/ffmpeg/overlay.go`, RenditionArgs -vf/-filter_complex split,
db.RenditionOverlay columns + validation, engine wiring, UI controls,
docs/RENDITIONS.md#watermarks.

**Design corrections, recorded in docs/roadmap/OVERLAYS.md:**
1. `scale2ref` is DEPRECATED in FFmpeg 8.1.2 and warns every start. The
   replacement (two-input `scale=rw:rh`) needs a `split` costing a frame copy
   per frame. Width computed in Go instead -> **an overlay requires explicit
   width AND height**, refused at validation time.
2. Columns, not the designed join table. 1:1 in v0.5; migration later is 6 cols.
3. **`r.Deinterlace` was missing from `renditionSig`** (dates from PR #12):
   changing the mode was saved and never encoded. Fixed, with a test naming
   every filter-changing field.

**Measured:** 18 rendered anchor x canvas combinations, +/-2px bbox. Swapping
the margin axes fails 17 of 18. Scale invariance proven at 1280x720 vs 720x1280.

## Branch stack, current

```
main
 └─ docs/restructure           PR #11
     └─ feat/rendition-ui-controls  PR #12   item 0
         └─ feat/playlist-phase-0   PR #13   item 1
             └─ feat/hls-latency        PR #14   item 2
                 └─ feat/destination-settings PR #15  doc + Kick key
                     └─ feat/mqtt-telemetry     PR #16  item 3
                         └─ feat/overlays-v05       PR #17  item 6 + audio flake fix
                             └─ feat/unreachable-settings PR #18
                                 └─ feat/dest-transport     PR #19  Part A
                                     └─ feat/dest-resilience   PR #20  Part C
                                         └─ feat/dest-audio        PR #21  Part B
```

## DONE this session (PRs #18-#20)

**PR #18 — three unreachable settings blocks.** The rendition drift guard only
walked db.Rendition, so the whole SETTINGS tree was unguarded.
`TestUITypesCanNameEverySettingsField` walks it recursively and found: failover
(incl. slate.imagePath, the field originally asked about), MQTT (no UI at all --
MY regression from PR #16), and the MQTT broker password having NO API ENDPOINT
whatsoever. Password gets its own route because the settings blob travels
outward on every read. `recording.stems` was a FALSE POSITIVE -- reachable via a
local `as {...}` cast. Guard limits documented in the test.

**PR #19 — Part A, transport/muxer.** -rw_timeout (confirmed ED, settable on an
output, parses on rtmp://), -flvflags no_duration_filesize (RTMP only), and the
muxing-queue PAIR. **Roadmap correction:** the doc had max_muxing_queue_size and
muxing_queue_data_threshold as ALTERNATIVES; FFmpeg's help says the threshold is
"the threshold after which max_muxing_queue_size is taken into account", so a
threshold alone does nothing and the validator refuses it. Also added a
Destination drift guard; the 3 expert-mode fields it flags are FALSE POSITIVES
(reachable via ExpertArgs{inputArgs,...}), skip-listed with reasons.

**PR #20 — Part C, resilience.** Per-destination backoff, supervisor
MaxRestarts (CONSECUTIVE, resets on a healthy run, stays given up), and
StartDelay for staggered go-live (first spawn only; interruptible by Stop).
Resilience had to be added to the destination restart signature BY HAND because
it is a supervisor property with no trace in the argv -- the r.Deinterlace bug
in a new place.

**PR #21 — Part B, audio.** Two of three shipped; the third REFUTED.
**AAC profile is not buildable**: FFmpeg's native aac encoder has NO -profile
option and `-profile:a aac_he` -> "Profile not supported!" with no output at
all. HE-AAC needs nonfree libfdk_aac. The GOAL (good audio under 64 kbps) is met
by Opus instead, which is free and already in the build. Opus is REFUSED on
RTMP: FFmpeg will mux it into FLV (probe produced a valid 8.6 KB file, Enhanced
RTMP defines a mapping) and no mainstream ingest accepts it. Mono turned out NOT
to need a routing-matrix change -- it is a downmix (`-ac 1`) of the operator's
stereo mix; re-routing tracks to one channel is a different feature.

## NEXT TASK — Part D, then overlays v1

**Part D, metadata (~7-12 d).** The compliance items first, since they are the
only ones with a legal edge: selfDeclaredMadeForKids (COPPA), privacyStatus,
Twitch content_classification_labels. Traps already researched and written down
in the roadmap doc -- read them, especially:
  - liveBroadcasts.update needs FOUR properties on EVERY call
  - it is destructive BY PART, not by field: `part=status` without privacyStatus
    reverts privacy to the default. A naive PATCH can make a private broadcast
    public.
  - most contentDetails toggles freeze once the broadcast leaves created/ready
  - selfDeclaredMadeForKids is insert-only; set it via videos.update afterwards
  - Twitch CCLs READ as a flat list and WRITE as [{"id":..,"is_enabled":true}].
    MatureGame is readable and NOT writable.

**Overlays v1 (~10 d of the original 16).** `docs/roadmap/OVERLAYS.md`. Text,
clock, externally-fed text, multiple overlays per rendition, the one-frame
preview endpoint, the embedded font, and the drawtext filter probe (detect.go
parses -encoders only; it needs -filters). Use `textfile=`, NEVER `text=` --
lavfiEscaper does not escape `%`, which drawtext expands.

**A fifth thing found and NOT done:** SettingsPage does not edit db.Settings
.Playout or .PostProd either; those have their own pages (PlayoutPage,
JobsPage), so they are probably fine, but nobody has checked field by field.
The Settings drift guard only proves NAMEABLE, not REACHABLE.

All five items the user asked for (0, 1, 2, 3, 6) are shipped as PRs #12-#17.
Remaining known work, none of it started:

- **DESTINATION-SETTINGS parts A-D** (`docs/roadmap/DESTINATION-SETTINGS.md`).
  Recommended order is in that doc; compliance metadata (made-for-kids,
  privacy, Twitch CCLs) is item 2 there and the only part with a legal edge.
- **Overlays v1** — text, clock, fed data, preview endpoint, embedded font,
  drawtext filter probe. ~10 d remaining of the original 16.
- **A FIFTH unreachable feature found and NOT fixed:**
  `SlateSettings.ImagePath` has no UI control at all. The drift guard in
  `internal/db/limits_drift_test.go` only covers `Rendition`, so nothing
  catches it. Extending that guard to Settings would likely find more.
- **Items 4, 5, 7, 8** from the roadmap were never in scope.

## (superseded) Kick stream key notes

Branch `feat/destination-settings` (already created, doc committed on it).

1. `internal/oauth/kick.go:83` `Scopes()` — add `streamkey:read`.
2. Fetch `stream.key` and `stream.url` from `GET /public/v1/channels` in the
   existing channel-fetch path (~kick.go:183 area, which already calls it and
   already has a scope-advice error message).
3. Wire into the same "refresh key" path YouTube/Twitch use.
4. **Adding a scope does not upgrade an existing token** — kick.go:74-76 says so
   already. The UI must tell the operator to disconnect and reconnect once,
   exactly as the Twitch chat-scope note does.
5. Correct `docs/PLATFORMS.md` in THREE places: the capability matrix row
   (`Stream key` = By hand -> Works), the "Kick — sign in *and* paste a key"
   paragraph, and the Kick setup step 4. Also `docs/COMPARISON.md` if it repeats
   it, and `README.md` if it does.

## Item 3 — MQTT

Design: `docs/roadmap/MQTT.md`. Build **retained telemetry first**; alert
delivery is a rider. Telemetry alone is 5–6 days and most of the value.

**Traps already found — these are the whole reason the design is trustworthy:**
- **`Queue: nil` is a no-op.** `autopaho/auto.go:271` substitutes
  `memory.New()`. Suppressing buffering needs an explicit no-op queue.
- **`paho.golang` is MQTT 5.0 only.** Say so in the docs and the UI.
- Everything QoS **1**, not 0 — a broker may decline to store a retained QoS 0
  message.
- `Slug()` must append 4 hex of sha256 whenever it changes the input, or
  `Twitch (main)` and `Twitch [main]` collide and silently overwrite each other.
- Line anchors into `internal/alerts` in the design drifted 30–60 lines, and
  `Coalescer` is actually an unexported `coalescer`. **Re-derive anchors.**
- `engine.Status` carries no source *name* — only `SourceID()` and `Source()`.
  The topic tree needs one; close that gap first.

## Item 6 — Overlays v0.5

Design: `docs/roadmap/OVERLAYS.md`. **v0.5 scope only** — see decisions table.

**Key finding that makes this cheaper than expected:** the
`hwupload`/`hwdownload` sandwich does **not** exist here. `prof.vaapi` is true
for exactly one encoder and there is no `-hwaccel` on input, so NVENC / QSV /
VideoToolbox / AMF all filter in system memory. An overlay is just another
software stage appended before VAAPI's one-way `format=nv12,hwupload` tail.

- Image overlays must be real `-i` inputs. **`movie=` is forbidden** —
  `synth.go:345` explains why (paths contain filtergraph separators).
- Geometry is **percentage-based**, non-negotiable: the same row must be correct
  at 1920×1080 and 1080×1920.
- **`RenditionArgs` with no overlays must be byte-identical to today.** The
  1,117-line `rendition_test.go` is the regression net.
- No `-loop 1`, no `-shortest`. Use `overlay=...:eof_action=repeat`.
- Pin `format=yuv420p` after the last overlay.

---

## Environment gotchas (cost real time this session)

- **`rm`, `cp`, `mv` are interactive aliases.** They prompt and default to *no*.
  Use `/bin/cp -f`, `/bin/mv -f`. A silent "not overwritten" looks like success.
- **A Docker container `poly-live` holds UDP 6000.** Re-confirmed 2026-07-28
  with `lsof -nP -iUDP:6000`; still the same container. This makes
  `internal/api`'s `TestTokenEnforcedTracksTheListenerNotTheSetting` fail
  locally — the listener cannot bind so `tokenEnforced` reads false. **It fails
  identically on a clean base. Not a real failure.** Stopping the container was
  denied by the permission classifier; do not retry.
- **`npm run build` wipes `internal/web/dist/` including `.gitkeep`**, which
  breaks `go build` in a clean checkout with
  `pattern all:dist: no matching files found`. `git checkout
  internal/web/dist/.gitkeep` after any UI build.

  **This actually happened, and cost five branches.** A `git add -A` in
  a89708a (PR #13) staged the DELETION of the tracked file, so every branch
  above it had no dist/ at all. It went unnoticed because gofmt was failing
  first in the same CI job and `go build` never ran -- one red check hid
  another. Restoring the working-tree file is not enough; check
  `git ls-tree <branch> internal/web/dist/` actually lists it.

  **Verify from a clean clone, not the working tree.** A deleted tracked file
  is invisible to a local build because the file is still on disk.

- **EVERY BRANCH IN THE STACK IS ITS OWN CI TARGET.** Checking the tip is not
  checking the stack: a later commit's `gofmt -w` silently fixes an earlier
  branch's file, so the tip is clean and the middle branch is red. This cost a
  round trip on `feat/mqtt-telemetry`. Loop over all of them, from a clean
  clone:

  ```sh
  W=/tmp/stackcheck; rm -rf $W; git clone -q --no-local . $W; cd $W
  for b in docs/restructure feat/rendition-ui-controls feat/playlist-phase-0 \
           feat/hls-latency feat/destination-settings feat/mqtt-telemetry \
           feat/overlays-v05; do
    git checkout -q "$b"
    fmt=$(gofmt -l ./cmd ./internal)
    [ -n "$fmt" ] && { echo "$b GOFMT: $fmt"; continue; }
    go build ./... >/dev/null 2>&1 && go vet ./... >/dev/null 2>&1 \
      && echo "$b clean" || echo "$b BUILD/VET FAILED"
  done
  ```
- Bash tool cwd persists between calls. `cd ui` then a repo-root command fails.
  Use absolute paths.

## Reading CI results (cost several round trips to learn)

- **`gh pr checks` renders `cancelled` as `fail`.** Every force-push kills
  in-flight runs, so a rebased stack accumulates phantom failures that look
  exactly like real ones. Distinguish them with the check-runs API, where
  `cancelled` and `failure` are separate values:
  `gh api repos/OWNER/REPO/commits/$SHA/check-runs --jq '.check_runs[] | select(.conclusion=="failure") | .name'`
- **A skipped step is not a passing step.** The static-checks job runs
  gofmt -> build -> vet -> test in sequence. gofmt failed, so `go build` never
  ran and showed as `-`. Two separate defects were hidden this way, one behind
  the other.
- **SonarCloud scores "New Code" relative to EACH PR's OWN BASE.** The tip of a
  stack can pass while the branch below it fails on identical code, because the
  code is not "new" to the tip. A green tip says nothing about the stack.
- **SonarCloud is advisory here** -- `main` is not protected, so it does not
  block merge. Its current complaint is the shell rule "use `[[` instead of
  `[`", which it files under *reliability*. The repo uses `[` uniformly (zero
  `[[` in any acceptance script) and every use is quoted, so this was
  deliberately NOT changed.
- Annotation `message` fields are just a URL; the actual rule is in `title`.

## Known flakes (pre-existing, not caused by the roadmap work)

- **`acceptance-audio`**: FIXED. My first guess -- an annotation/recorder
  ordering race -- was WRONG. The CI artifacts (always download them:
  `gh api repos/O/R/actions/artifacts/<id>/zip`) showed the real cause:
  until the ingest is probed, `e.source` is `routing.DefaultSource()`, which
  has SIX placeholder tracks. Both the recorder's stem plan and the meters'
  filtergraph compiled that onto a command line, so FFmpeg got `-map 0:a:3`
  on a three-track ingest and refused to start. Both tiers crash-looped from
  startup on EVERY run, including passing ones -- on a 2-core runner that
  becomes a restart storm and the outcome is a race. Both now wait for a real
  layout (`effectiveSourceKnown`). Suite also gained EXPECTED_CHECKS=35.
- **`internal/relay` TestStatsReportsNoLossForACleanStream**: FIXED. The test
  waited on rxPackets and asserted TSPackets; run() increments rxPackets
  before measuring, so it raced the last datagram. `waitForTS` added.

## Verification commands

```sh
# gofmt FIRST. CI runs it as a hard gate and it is the one check that passes
# locally by being forgotten -- go vet and go build both ignore formatting, so
# a misaligned struct literal ships green and fails CI.
gofmt -l ./cmd ./internal   # must print nothing
go vet ./... && go test ./... -count=1
cd ui && npx tsc -b && npx oxlint src/ && npm run build   # then restore .gitkeep
node /private/tmp/claude-501/-Users-rainmanjam-Documents-polyemesis/d9a3c604-ce41-46ff-8f77-73e05332b6c7/scratchpad/linkcheck.mjs
```

The link checker models GitHub's slug algorithm, including that it does **not**
collapse the double space an em dash leaves behind. Keep it — 246 links pass.

## House rules that matter here

- **Measure, do not assert.** Audio proved by bandpass RMS per track, video by
  frame-level `framemd5`. "Returned no error" proves nothing.
- **Include the positive case.** A check that only tries bad input passes just as
  happily when the feature refuses everything.
- **Fixed-value guard on suite check counts** (`EXPECTED_CHECKS`), because a
  suite once reported "7 passed, 0 failed" having skipped five.
- **`die()` writes to stderr**, never stdout — stdout is a value channel.
- Comments explain *why*, and especially *why not*, with the measurement that
  settled it.
