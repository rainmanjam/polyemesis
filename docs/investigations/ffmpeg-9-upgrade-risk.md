# Upgrading FFmpeg 8.1.2 → 9.0.1

Measured on 2026-08-27, against the actual binaries, not reasoned about.

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
