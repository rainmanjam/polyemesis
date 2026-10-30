# Upgrading FFmpeg 8.1.2 → 9.0.1

Measured on 2026-08-27, against the actual binaries, not reasoned about.

## Update, 2026-10-29: 9.0.2

**9.0.2 is the 9.x to take, and 9.0 and 9.0.1 are the two to avoid.** Both
shipped "fftools/ffmpeg_sched: allow throttling decoder outputs" (03dfac5630).
9.0.2 reverts it ([9d4ca2122](https://github.com/FFmpeg/FFmpeg/commit/9d4ca2122))
because it can misorder the output interleave, end streams early and use extreme
memory. A destination is one input, several audio decoders and one filtergraph,
which is the shape that goes through that path. The 9.0.1 result recorded below
pre-dates the revert, and `Detect` now prints a startup warning for 9.0/9.0.1
(`Tools.KnownRegressionWarning`). 8.1.x never had the change: GitHub's compare
of 03dfac5630 against n8.1.3 says "diverged".

**The packaging blocker is narrower, but still there.** Alpine edge now has
`ffmpeg 9.0.2-r0`, with VAAPI linked. No stable branch does: 3.24 moved to
**8.1.3-r0**, which broke the `8.1.2-r0` pin and every image build until the
Dockerfile was bumped. Putting edge onto 3.24 was measured, not assumed:

| route | result |
|---|---|
| `apk add --repository=…/edge/community ffmpeg=9.0.2-r0` on 3.24 | **fails**: needs `libx265.so.216`, `libvpx.so.12`, `libbluray.so.4`, which 3.24 lacks |
| edge/main + edge/community on 3.24 | installs, and srt, VAAPI, libx264 and the filters are all present. But **104 of 107 packages come from edge** (libstdc++, libgcc, x264, x265, libsrt, libva, glib…) on 3.24's musl and openssl. That userland is unsupported, and it changes under the same tag whenever edge moves |
| `alpine:edge` base | works and keeps VAAPI, but it is a rolling distribution under a release image |

So the image stays on 8.1.3 until 3.25 (or whichever stable branch comes next)
packages 9.x. At that point the change is the FROM line plus the pin.

Ubuntu has no 9.x in any release: 26.04 has 8.0.1, and 26.10 and devel have
8.1.2. So `Dockerfile.vaapi` and `Dockerfile.cuda` stay where they are. Debian
sid has 9.0.2-4. johnvansickle.com still publishes 7.0.2. BtbN publishes n9.0
for linux64, linuxarm64, win64 and winarm64, both in the rolling `latest` and
in dated releases. The month-end `autobuild-2026-09-30-13-08`, which BtbN keeps,
has `n9.0.2-17-g2a571b6068`.

**What changed between 8.1.2 and 9.0.2 that touches us.** This comes from a diff
of the two source trees, file by file, over everything we drive.

- `fftools/ffmpeg.c` is **byte-identical**. That covers `-progress` output
  (`print_report`), the stdin `c` command handling
  (`check_keyboard_interaction`), `-stats_period` and the main loop. The
  `Command reply for stream N: ret:R res:` line in `ffmpeg_filter.c` is
  unchanged too.
- Removed CLI options (`FFMPEG_OPT_*` dropped at the major bump): `-vsync`,
  `-filter_script`, `-filter_complex_script`, `-top`, `-qphist`,
  `-adrift_threshold`, plus the `drop` value of the vsync/fps_mode parser and
  `source_no_drop` for `-force_key_frames`. Measured: `-vsync` and
  `-filter_complex_script` exit 8 with "Unrecognized option" on 9.0.2. **None is
  in our argv.** An operator's expert arguments can carry them, and such a
  destination will not start on 9.x.
- NVENC: the named presets, the `*_hq` and `2pass` rate-control modes, `-cbr`
  and `-2pass` are removed, and the SDK floor is now 11.1. Our argv uses
  `-preset p4` and `-rc cbr`/`vbr`, so it is unaffected. See HARDWARE.md.
- libsrt: there is a new `ipv6only` option, and on SRT ≥ 1.5.2 an IPv6
  **wildcard** listener without it is an error. Our listener binds
  `srt://0.0.0.0:…`, so it is unaffected. Watch for this if the bind address
  ever becomes `[::]`.
- `af_pan`: a new error for an unsupported input channel id. `af_aresample`:
  ENOMEM handling only. `segment`/`tee`: program transfer to child muxers.
  `concat`: a recursion-depth limit (10). `dashenc`: two new options. flv,
  mpegts, matroska, hls, rtmp, amix, volume, loudnorm, alimiter, adelay, atrim,
  asplit, ebur128, astats: no change, or no change to behaviour we depend on.
- `transpose_npp` is gone (replaced by `transpose_cuda`). We do not emit any
  `*_npp` filter.
- The version banner is unchanged in shape. `opt_common.c` now formats it with
  `%s`, and the output is still `ffmpeg version <x>`.

**The suites, 2026-10-29.** All runs set `POLYEMESIS_REQUIRE_FFMPEG=1`, so a
missing binary fails rather than skips. The packages are ffmpeg, supervisor,
engine, media, meters, clipper, clips, playlistmedia, transcribe, recording,
rtmpserver, srtserver, multitrack and uploadprobe.

| FFmpeg | where | pass | fail | skip |
|---|---|---|---|---|
| 9.0.2 (Homebrew) | macOS arm64 | 3101 | 1¹ | 12² |
| n9.0.2-17 (BtbN) | Linux container | 3134 | 1³ | 11² |
| n8.1.2-34-g9b6c8969e0 (BtbN, the staging build's commit) | Linux container | 3139 | 1³ | 4 |
| n6.1.3 (BtbN, the floor) | Linux container | 3126 | 7⁴ | 11 |

1. `transcribe` TestTheScratchWAVsAreRemovedEvenWhenTheJobFails. It globs the
   shared TMPDIR, the stub is `exit 1` with no FFmpeg involved, and it passed
   5/5 in isolation.
2. Seven rtmpserver #674 reproductions skip by design on any major other than 8
   (`requireShippedFFmpeg`). The 3 drawtext skips are a Homebrew build without
   libfreetype.
3. `meters` TestAnalyserMeasuresThePerDestinationMixAndNotTheIngest ("never
   bound udp … in 10s"). It fails identically on all three versions in the
   container, so it is the harness artefact recorded below, not a version
   regression.
4. The meters artefact, plus the documented below-7.1 limits: multitrack FLV
   (×1), two audio streams in flv (×3 with subtests), and SIGTERM ignored on a
   quiet feed (×2).

**I4 live gain on each version.** There are two checks. The first is the
supervisor's real-FFmpeg test: mute, then gain 2.0, with the RMS measured and
0 restarts required. It passes on 9.0.2, 8.1.2 and 6.1.3, with reply latencies
of 8–54 ms. The second is end to end: the built binary, a synthetic 3-track
ingest, a file destination, and then `POST
/destinations/1/tracks/0/gain` for gain 0.5, then mute, then unmute at 2.0. On
**8.1.2, 9.0.2 and 6.1.3**, all three changes report `"applied": "live"` and
status `levelChange: "applied live"`, with the **same pid and restarts=0**. The
live path does not need a version gate.

## Decision: staying on 8.1.2 (2026-08-27)

Taken deliberately, not deferred. The behaviour of 9.0.1 is fine — the
version-sensitive suites pass against it — but Alpine has not packaged any 9.x,
and the route around that costs VAAPI. Waiting keeps hardware encoding for
Intel and AMD hosts, keeps apk's signing and Alpine's CVE tracking, and costs
only time.

**Revisit when Alpine packages 9.x**, at which point most of this file stops
applying: the packaging problem disappears, `apk add ffmpeg=9.x` keeps VAAPI,
and what remains is the behavioural check, which has already been done once
here and can be repeated with the same commands.

The rest of this file is the evidence behind that, kept so the next person —
or the dependabot PR that eventually offers this — starts from the answer
rather than rediscovering it.

**Recommendation, as measured: not yet, and the blocker is packaging rather
than behaviour.**

## The blocker

`Dockerfile` installs FFmpeg with `apk add "ffmpeg=${FFMPEG_VERSION}"`, and
**Alpine has not packaged any 9.x — including `edge`:**

| base | ffmpeg |
|---|---|
| `alpine:3.23` | 8.0.1 |
| `alpine:3.24` (what we pin) | 8.1.2 |
| `alpine:edge` | **8.1.2** |

So this is not a version-string bump. Taking 9.0.1 means changing where FFmpeg
comes from: a static build downloaded and checksum-verified in the image, a
different base distribution, or building from source. Each of those is a larger
change than the upgrade itself, and each replaces `apk`'s signing and CVE
tracking with something we would then own.

Upstream and other distributions do have it — `n9.0.1` is tagged, `n9.1-dev` is
open, and Arch, Homebrew, nix-unstable and debian-experimental all ship 9.0.1.
Alpine will get there; the cheap version of this upgrade is the one that waits
for that.

## The behaviour, measured

Every Go package that shells out to FFmpeg, run against BtbN's
`n9.0.1-9-gfa97c9f046` static build:

```
ok    internal/ffmpeg        33.9s     <- the package encoding every measured behaviour
ok    internal/playlistmedia  9.1s
ok    internal/multitrack     0.2s
ok    internal/clipper        3.7s
ok    internal/recording      1.2s
ok    internal/uploadprobe    0.0s
FAIL  internal/meters        10.0s
```

**The `meters` failure is not a 9.0 regression.** The same test fails
identically in the same container on `n8.1.2-47-g156bb4d299`:

```
the analyser never bound udp 127.0.0.1:PORT in 10s
```

It is an artefact of running this suite against a read-only bind mount in a
container. Without that control it would have been reported as a regression,
which is the whole reason the control was run.

`internal/ffmpeg` is the package that carries the version-sensitive knowledge —
the filter graphs, the probe parsing, the argv construction — and it passes.

## What 9.0 actually removed

The 9.0 changelog is almost entirely additive. Three removals:

- CELT decoding (explicitly *not* Opus CELT) — we do not use it
- ogg/celt parsing — we do not use it
- **deprecated NVENC options, and support for pre-11.1 SDK versions**

Only the third could touch us, and it does not: `internal/ffmpeg/rendition.go`
already uses the current option set — `-preset p4` from the p1..p7 series that
replaced the named presets, and `-rc cbr` / `-rc vbr` rather than the
deprecated `vbr_hq` / `ll` spellings. The code comment already says "NVENC's
p1..p7 presets replaced the named ones".

The SDK floor is a host-side consideration rather than an argv one: an operator
on a pre-11.1 NVIDIA driver would lose hardware encoding. That is worth a
release note if this upgrade happens, not a code change.

**API churn does not apply here.** polyemesis builds with `CGO_ENABLED=0` and
invokes the `ffmpeg` and `ffprobe` binaries as subprocesses. It links no
`libav*`, so a major-version ABI break is not a category of risk for us — only
CLI and filter behaviour is.

## What is still unmeasured

The nine places in the tree that say "measured against FFmpeg 8.1.2" are the
real surface. The unit suites cover most of them, and pass. Not covered by the
run above:

- the acceptance suites, which push real streams end to end — `acceptance-failover`,
  `acceptance-obs-multitrack` and `acceptance-encoders` each encode a measured
  8.1.2 behaviour in a comment
- hardware encoders (NVENC, QSV, VAAPI, VideoToolbox), which no container here
  can exercise — see #375
- the `amerge` channel ceiling, which already moved once between 6.1.1 and 8.1
  (80 → 64) and is the kind of limit that moves again

## How it would actually be done

The packaging question has an answer, and it is a two-line multi-stage stage.
Measured, not sketched.

### What does not work

**BtbN's builds do not run on Alpine.** They are described as static and are
not: the arm64 binary reports

```
ELF 64-bit LSB pie executable, ARM aarch64, dynamically linked,
interpreter /lib/ld-linux-aarch64.so.1, for GNU/Linux 4.18.20
```

On musl that is `sh: /ff9/bin/ffmpeg: not found` — the classic missing-glibc-loader
error, which reads as a missing file and is not one. CI can use these because
its runners are Ubuntu; the image cannot.

### What does work

`mwader/static-ffmpeg` publishes genuinely static musl builds, multi-arch, tagged
by FFmpeg version — `9.0.1`, `9.0.1-amd64`, `9.0.1-arm64` all exist:

```dockerfile
FROM mwader/static-ffmpeg:9.0.1 AS ffmpeg
FROM alpine:3.24
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
```

Verified: `static-pie linked`, runs on `alpine:3.24`, encodes h264/aac, carries
libx264, libx265, libvpx-vp9, libopus, libsvtav1 and the srt/rtmp/rtmps
protocols. `internal/ffmpeg`, `playlistmedia`, `clipper` and `multitrack` all
pass against it.

### The cost, and it is a real one

| | Alpine `apk` 8.1.2 | static musl 9.0.1 |
|---|---|---|
| encoders | 220 | 225 |
| `h264_vaapi` | **yes** | **no** |
| `h264_qsv` | no | yes |
| `h264_nvenc` | no | no |
| libx264 / x265 / vpx-vp9 / opus / svtav1 | yes | yes |
| srt / rtmp / rtmps | yes | yes |

**VAAPI is lost, and that is inherent rather than a packaging choice.** VAAPI
needs to `dlopen` libva at runtime and a fully-static binary cannot. polyemesis
offers VAAPI as a rendition encoder (`internal/ffmpeg/rendition.go` marks the
encoders needing a device and a hwupload tail), so this would remove hardware
encoding for Intel and AMD GPU hosts running the container.

Neither build has NVENC, so nothing changes there — NVENC users are already on
binary mode with their own FFmpeg.

Three other things the switch takes on:

- **apk's signing and CVE tracking go away.** `apk add ffmpeg=8.1.2-r0` is
  verified against Alpine's keys and tracked by Alpine security. A `COPY --from`
  of a third-party image is trusted because we chose to trust it, so the tag
  wants pinning by digest and something has to watch it.
- **Image size.** A static FFmpeg carries every codec it was built with.
- **One more upstream.** mwader's cadence becomes ours.

### The honest ordering

If VAAPI matters — and for a self-hosted multistreamer on Intel hardware it
plausibly does — then the static route is not an upgrade, it is a trade, and it
should be presented as one rather than taken quietly. Waiting for Alpine keeps
VAAPI, keeps apk's signing, and costs nothing but time.

If it does not matter, the three lines above are the whole change, and the
behavioural evidence says 9.0.1 is safe.

## If it is taken anyway

1. Solve packaging first, deliberately and separately: it is the larger half
   and it is not a media problem.
2. Run the acceptance matrix against 9.0.1 before the pin changes, not after.
3. Re-verify the nine measured comments and update them to say 9.0.1, or delete
   the ones that stop being true. A comment citing a version the product no
   longer runs is worse than no comment.

## Reproducing this

```sh
# 9.0.1
docker run --rm -v "$PWD:/repo:ro" -v /path/to/ff9:/ff9:ro golang:1.27-bookworm \
  sh -c 'export PATH=/ff9/bin:$PATH; cd /repo && go test ./internal/ffmpeg/ ...'
```

Static builds: `BtbN/FFmpeg-Builds`, release tag `latest`, assets
`ffmpeg-n9.0-latest-linuxarm64-gpl-9.0.tar.xz` and the matching `n8.1` one for
the control. Always run the control.
