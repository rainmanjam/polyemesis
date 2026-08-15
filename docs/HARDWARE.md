# Hardware encoding

> **EXPERIMENTAL — no NVENC, QSV or VA-API encode has ever been observed by this
> project.** Every claim on this page about a GPU encoding a frame is reasoned
> from FFmpeg's source and option tables, or reproduced in a container with no
> GPU attached; see [What has actually been run, and what has
> not](#what-has-actually-been-run-and-what-has-not), which is the precise
> boundary. Hardware encoding is fully enabled, requires no opt-in, and the
> probe still refuses an encoder that cannot open — what is unverified is
> whether the encoders that *do* open behave as this page says once they are
> running.

polyemesis re-encodes video in one place — the **rendition** — so a single 4K60
ingest can feed platforms with lower ceilings without you running four
encoders. That only works if the encode keeps up with realtime, and `libx264`
at 4K60 does not keep up on a normal machine. Hardware encoding is what makes
the feature usable, which is why this page exists.

The goal is the one datarhei Restreamer sets: **adjust to whatever GPU is
present at launch — Intel, NVIDIA, AMD or none — with nothing for you to
configure.** Where that fails, it fails into software encoding and a stream
that still goes out.

- [The one thing to understand first](#the-one-thing-to-understand-first)
- [What each vendor needs](#what-each-vendor-needs)
- [Running in Docker](#running-in-docker)
- [Checking it yourself](#checking-it-yourself)
- [Troubleshooting, by the error you got](#troubleshooting-by-the-error-you-got)
- [What has actually been run, and what has not](#what-has-actually-been-run-and-what-has-not)

---

## The one thing to understand first

**`ffmpeg -encoders` is a list of what the binary was compiled with. It is not
a list of what your machine can do.** Those are different questions and they
have different answers, routinely, in both directions.

Measured, not assumed:

| Machine | `ffmpeg -encoders` lists | Actually encodes a frame |
|---|---|---|
| A stock `ubuntu:24.04` container with **no GPU at all** | `libx264`, `h264_nvenc`, `hevc_nvenc`, `h264_qsv`, `hevc_qsv`, `h264_vaapi`, `hevc_vaapi` | `libx264` only |
| A macOS development machine (Homebrew FFmpeg) | `libx264`, `libx265`, `h264_videotoolbox`, `hevc_videotoolbox` | all four — and no `_nvenc`, `_qsv`, `_vaapi` or `_amf` exists to try |

The Linux row is the dangerous one. A stock FFmpeg advertises NVENC, Quick
Sync and VA-API on a box with no graphics hardware whatsoever, because those
encoders are wrappers that `dlopen()` a vendor library at *runtime*. Compiling
the wrapper in costs nothing and commits to nothing.

The macOS row is the same mismatch running the other way, which is why the
answer cannot be a table of assumptions either: the list is *short* there, and
a build that omits an encoder is as real a constraint as hardware that cannot
run one. Only asking the machine covers both.

So polyemesis does not read the list. At startup it **encodes one frame with
each candidate** and keeps the exit status:

```bash
ffmpeg -f lavfi -i testsrc2=size=320x240:rate=1 -frames:v 1 \
       -c:v h264_nvenc -f null -
```

Exit 0 means that encoder works on this machine, right now, with these
drivers. Anything else means listed-but-unusable, and the rendition editor
greys it out with the reason FFmpeg gave — before you go live, rather than
after.

The candidates are the **five H.264 hardware encoders plus `libx264`** — six
probes, run concurrently. The HEVC encoder of each family is not probed: it
opens the same device through the same driver as its H.264 sibling, so the
sibling's exit status answers for it, and the editor marks that verdict as
inferred rather than measured.

Three consequences worth knowing:

- **Detection never blocks startup.** A probe that fails, times out or finds
  nothing at all leaves you on software encoding with everything else working.
  Hardware detection is not allowed to be the thing that stops your stream.
- **The verdict is re-checkable, and it separates the two questions.**
  `GET /api/v1/encoders` returns every candidate with `available` ("does this
  binary contain the encoder"), `works` ("did it encode a frame here just
  now"), the `reason` when it did not, and `durationMs`. The gap between those
  first two fields is the whole problem, so the API refuses to collapse them.
  `GET /api/v1/encoders?redetect=1` re-runs the scan — plug a GPU in, install a
  driver, fix a permission, then re-detect, no restart needed.
- **A working hardware encoder becomes the default for a new rendition**, in
  the order VideoToolbox, NVENC, QSV, VA-API, AMF, then `libx264`. "Working"
  means it passed the probe — the build merely listing an encoder is not
  evidence, and defaulting to a listed-but-dead one is how an operator finds out
  about `libcuda` after going live. If every probe fails, including x264's, the
  default is `libx264` anyway: that keeps the product usable and the failure
  legible, where an empty `-c:v` is neither.

  You can still choose `libx264` per rendition, and there are reasons to — its
  behaviour is identical everywhere, while hardware wrappers vary by driver
  version. But it is an override, not the starting point.

---

## What each vendor needs

| Vendor | Encoders | What the **host** needs | What the **container** needs |
|---|---|---|---|
| Software | `libx264`, `libx265` | Nothing. CPU only. | Nothing. Works in the default image. |
| **NVIDIA** | `h264_nvenc`, `hevc_nvenc` | A GPU with an NVENC engine; the **proprietary** NVIDIA driver (nouveau has no NVENC); `nvidia-container-toolkit` if you use Docker | `Dockerfile.cuda`; `--gpus all`; `NVIDIA_DRIVER_CAPABILITIES` must include **`video`** |
| **Intel** (Quick Sync) | `h264_qsv`, `hevc_qsv` | An iGPU or Arc with Quick Sync — F-suffix desktop parts have it fused off; `/dev/dri/renderD*` present; the iHD driver | `Dockerfile.vaapi`; `--device /dev/dri:/dev/dri`; `--group-add` the render node's **numeric GID** |
| **Intel / AMD** (VA-API) | `h264_vaapi`, `hevc_vaapi` | `/dev/dri/renderD*` present; the iHD driver for Intel (`intel-media-va-driver` on Debian/Ubuntu, `intel-media-driver` elsewhere) or Mesa's `radeonsi` for AMD | Same as above — `Dockerfile.vaapi`, `--device /dev/dri`, `--group-add <gid>` |
| **AMD** (AMF) | `h264_amf`, `hevc_amf` | The AMF runtime. In practice this is the **Windows** path: Ubuntu's FFmpeg is not built with AMF at all (verified — there are no `*_amf` encoders in it), so AMD on Linux goes through VA-API | Not offered by any image here. Use VA-API. |
| **Apple** | `h264_videotoolbox`, `hevc_videotoolbox` | Nothing. Every Mac that runs FFmpeg can do this; no device node, no driver setup | Not applicable — Docker on macOS is a Linux VM with no access to the media engine. Run the binary natively. |

Notes that save time:

- **Quick Sync vs VA-API on Intel.** `h264_qsv` goes through Intel's oneVPL/MFX
  runtime; `h264_vaapi` goes through libva. Both end up at the same silicon.
  VA-API has fewer moving parts in a container, so if only one of them probes
  clean, take the one that works.
- **Most RTMP ingests accept H.264 only.** The HEVC encoders are real and
  occasionally useful (SRT, a file destination, an ingest you control), but a
  live platform is unlikely to take one.
- **On Windows,** NVENC, Quick Sync and AMF are reached through the graphics
  driver. There is no device to pass through and no permission to get wrong: if
  an encoder is missing, update the GPU driver.

---

## Running in Docker

Three images, because one image cannot do this. The default is Alpine, and
NVENC needs `libnvidia-encode.so.1` — part of the NVIDIA *driver*, not
something any package manager can install into an image. It arrives from the
host at `docker run` time or it does not arrive.

| File | Base | For |
|---|---|---|
| `Dockerfile` | Alpine | The default. CPU / `libx264`. Nothing to configure. |
| `Dockerfile.cuda` | `nvidia/cuda:*-base-ubuntu24.04` | NVIDIA / NVENC |
| `Dockerfile.vaapi` | `ubuntu:26.04` | Intel and AMD / VA-API and QSV |

`docker-compose.yml` carries all three as one file: the default service is
active, the two GPU variants are commented out just below it. Uncomment one,
comment out the default. (They share a `container_name`, so forgetting the
second half fails immediately and says so, rather than starting something
surprising.)

### NVIDIA

```bash
docker build -f Dockerfile.cuda -t polyemesis:cuda .
docker run --gpus all \
  -p 8080:8080 -p 6000:6000/udp -p 1935:1935 \
  -v polyemesis-data:/data \
  polyemesis:cuda
```

**`nvidia-container-toolkit` on the host is not optional.** It is the piece
that injects the driver libraries and `/dev/nvidia*` into the container, and
its absence is the usual cause of "NVENC does not work in Docker". Install it
from NVIDIA's repository, then:

```bash
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

Prove the host is right before blaming the image:

```bash
docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi
```

If that prints your GPU, the host is fine.

**The second trap is invisible.** The `nvidia/cuda` base images ship
`NVIDIA_DRIVER_CAPABILITIES=compute,utility` — verified by reading `env` in
`12.6.3-base-ubuntu24.04`. That set does **not** include `video`, so the
toolkit injects `libcuda.so.1` but not `libnvidia-encode.so.1`. `nvidia-smi`
works, CUDA works, and NVENC alone fails. `Dockerfile.cuda` sets
`compute,utility,video` for exactly this reason — but a compose
`deploy.resources.reservations.devices` entry **overrides** what the image
declared, so the compose block must list it too:

```yaml
deploy:
  resources:
    reservations:
      devices:
        - driver: nvidia
          count: all
          capabilities: [gpu, video]   # `video` is the one everybody omits
```

### Intel and AMD

```bash
docker build -f Dockerfile.vaapi -t polyemesis:vaapi .
docker run --device /dev/dri:/dev/dri \
  --group-add "$(stat -c '%g' /dev/dri/renderD128)" \
  -p 8080:8080 -p 6000:6000/udp -p 1935:1935 \
  -v polyemesis-data:/data \
  polyemesis:vaapi
```

`--device /dev/dri:/dev/dri` hands the render node over. Without it there is no
`/dev/dri` in the container at all, and VA-API and QSV both fail.

**Then there is the group-ID problem, which catches everyone.** On the host,
`/dev/dri/renderD128` is typically `root:render` mode `0660`: only root and the
render group may open it. polyemesis runs as an unprivileged user in the
container, so it needs that group — and the kernel matches group membership by
**numeric GID**, against the host's node. Two things follow:

- **The GID differs between distributions**, and between installs of the same
  distribution depending on what created the group first. No number is worth
  hardcoding and none is quoted here on purpose: any figure you copy from a
  blog post is a number that was true on someone else's machine. Neither
  Dockerfile guesses one either.
- **`--group-add render` usually fails outright**, because the *name* has to
  resolve inside the container and these base images have no render group:

  ```text
  docker: Error response from daemon: unable to find group render:
  no matching entries in group file
  ```

  (Verified against `ubuntu:24.04`, which has `video` at GID 44 and no `render`
  group at all.)

So read the number off the host's own device node — `stat -c '%g'
/dev/dri/renderD128` — and pass that. It is correct on every distribution
without you having to know which one you are on. In compose, where there is no
shell, print it and paste it into `group_add`.

`LIBVA_DRIVER_NAME` is deliberately left unset in the image. Unset means libva
probes the device and picks the driver that matches it, which is right on Intel
and AMD alike. A lot of copied-around Dockerfiles pin it to `iHD`, which breaks
every AMD host using the same image. Override it only for an old Intel part the
probe gets wrong (`-e LIBVA_DRIVER_NAME=i965`).

---

## Checking it yourself

The probe polyemesis runs, so you can run it by hand and read the whole error:

```bash
ffmpeg -f lavfi -i testsrc2=size=320x240:rate=1 -frames:v 1 \
       -c:v h264_nvenc -f null -
echo "exit=$?"
```

VA-API needs its device named before the input, and a format conversion, or it
fails for reasons that have nothing to do with your hardware:

```bash
ffmpeg -vaapi_device /dev/dri/renderD128 \
       -f lavfi -i testsrc2=size=320x240:rate=1 -frames:v 1 \
       -vf format=nv12,hwupload -c:v h264_vaapi -f null -
```

What the operating system thinks it has:

```bash
ls -l /dev/dri                       # render nodes, and their owning group
stat -c '%g %n' /dev/dri/renderD*    # the GID you need for --group-add
nvidia-smi                           # NVIDIA driver + GPU, host or container
vainfo --display drm --device /dev/dri/renderD128   # VA-API entrypoints
```

`vainfo` defaults to X11 and will fail with `can't connect to X server!` on a
headless host or inside a container. The `--display drm` above is what makes it
answer the question you meant to ask.

And what polyemesis itself concluded, which is the same probe with the results
already collected:

```bash
curl -s localhost:8080/api/v1/encoders             # cached verdicts
curl -s 'localhost:8080/api/v1/encoders?redetect=1'  # re-run everything
```

---

## Troubleshooting, by the error you got

Search your log for the line, not the symptom. Every entry below is a real
FFmpeg or Docker message.

> Messages marked **(reproduced)** were produced on 2026-07-26 by running the
> probe in a `ubuntu:24.04` container with no GPU. The rest were read verbatim
> out of the FFmpeg 6.1.1 libraries themselves (`strings libavcodec.so.60`),
> because the hardware needed to break in those particular ways was not
> available here — the wording is exact, the surrounding advice is not
> first-hand.

### `Cannot load libcuda.so.1` — (reproduced)

The NVIDIA driver's userspace is not visible to the process.

- **In Docker:** `nvidia-container-toolkit` is missing, not wired into the
  daemon, or you forgot `--gpus all`. This is the most common cause by a wide
  margin. Check with
  `docker run --rm --gpus all nvidia/cuda:12.6.3-base-ubuntu24.04 nvidia-smi`.
- **On bare metal:** the driver is not installed, or nouveau is loaded instead
  of the proprietary one. `lsmod | grep -E 'nvidia|nouveau'`.
- **On a machine with no NVIDIA GPU:** nothing is wrong. FFmpeg lists NVENC
  because it was compiled with it; the probe found out it cannot be used, which
  is the system working. Use `libx264` or VA-API.

### `Cannot load libnvidia-encode.so.1`

Distinct from the one above, and it means something much more specific: CUDA is
there and the *encode* library is not. In a container that is almost always
`NVIDIA_DRIVER_CAPABILITIES` without `video` — see [NVIDIA](#nvidia) above. The
tell is that `nvidia-smi` works fine inside the same container.

### `No CUDA capable devices found` / `No capable devices found`

Two different lines, both short, and the difference matters. Both are verbatim
from FFmpeg 6.1.1 — read out of `libavcodec.so.60`, so the wording here is the
wording you will see.

- **`No CUDA capable devices found`** — the driver loaded and enumerated zero
  GPUs. In a container that is `--gpus` missing or selecting nothing;
  `NVIDIA_VISIBLE_DEVICES` set to a UUID that is not on this host does it too.
- **`No capable devices found`** — GPUs were enumerated and none of them has an
  NVENC engine. Either the board genuinely lacks one (some datacentre compute
  parts, some very low-end ones — NVIDIA's "Video Encode and Decode GPU Support
  Matrix" is the authority on yours), or on a multi-GPU host `--gpus` selected
  the one that does not.

Searching for "No NVENC capable devices found" will find you nothing. That
phrasing is common in write-ups about this failure and is not what FFmpeg
prints.

### `The minimum required Nvidia driver for nvenc is <version> or newer`

### `Driver does not support the required nvenc API version. Required: X.Y Found: A.B`

Both verbatim from FFmpeg 6.1.1. The host driver is older than the NVENC API
this FFmpeg was built against. FFmpeg prints the exact version it wants — take
that number rather than any table, and update the **host** driver. Nothing
inside the container can fix this: the encode library comes from the host, so a
newer image is not the answer.

### `No VA display found for device /dev/dri/renderD128` / `Device creation failed: -22` — (reproduced)

The path does not exist or is not a usable render node.

- In Docker, this is a missing `--device /dev/dri:/dev/dri`. Confirm with
  `docker exec <container> ls -l /dev/dri` — if the directory is absent
  entirely, that is your answer.
- On bare metal, `ls -l /dev/dri` on the host. No `renderD*` at all means no
  kernel driver is bound to the GPU.
- On a multi-GPU host the first card may be display-only. Detection enumerates
  `/dev/dri` and picks a render node, preferring Intel and AMD over NVIDIA, and
  **the probe now tests that same node** rather than `renderD128` regardless.
  Until this was fixed the two halves disagreed: a machine whose usable node was
  `renderD129` had VA-API greyed out on evidence gathered from the wrong GPU.
  If detection finds no usable render node at all it still probes
  `/dev/dri/renderD128`, because a VA-API probe with no device named fails
  everywhere — including where VA-API works.

### `Permission denied` opening `/dev/dri/renderD128`

The node is there and the process may not open it. This is the group-ID problem
in [Intel and AMD](#intel-and-amd): add `--group-add "$(stat -c '%g'
/dev/dri/renderD128)"`. A *wrong* GID fails identically to no GID at all, so
re-read it from the host rather than reusing a number from somewhere else.

### `Failed to initialise VAAPI connection: <n> (<message>)`

libva found the display and could not bring it up. (British spelling, and
FFmpeg appends the numeric error and libva's own text — grep for
`initialise VAAPI` rather than typing the whole line.) Usually the VA-API driver
for your silicon is missing: `intel-media-va-driver` for Intel Gen9 and newer,
`i965-va-driver` for older Intel, `mesa-va-drivers` for AMD.
`vainfo --display drm --device /dev/dri/renderD128` will say which driver it
tried to load and why it failed. If it names `iHD` on an AMD card, something
has set `LIBVA_DRIVER_NAME` — unset it and let libva probe.

### `Impossible to convert between the formats supported by the filter 'Parsed_null_0' and the filter 'auto_scale_0'` — (reproduced)

Only if you are driving FFmpeg by hand: VA-API takes frames in GPU memory, so
the command needs `-vaapi_device` *before* the input and
`-vf format=nv12,hwupload`. polyemesis builds this correctly; a copy-pasted
command line often does not.

### `Error creating a MFX session: -9.` — (reproduced)

Quick Sync's runtime could not start. The number is an MFX status code, not an
errno — look it up rather than reading it as one. The situation behind it is
almost always one of: no Intel GPU, no render node passed through, or no
oneVPL/MFX runtime installed. This particular `-9` was produced by running the
probe in a container with no GPU and no `/dev/dri`.

If VA-API works on the same machine, use `h264_vaapi` instead — same silicon,
fewer moving parts, and one less runtime that has to be present.

### `unable to find group render: no matching entries in group file` — (reproduced)

From Docker, not FFmpeg, and it means you passed `--group-add render`. The name
has to resolve inside the container and these images have no render group. Pass
the host's number instead: `--group-add "$(stat -c '%g' /dev/dri/renderD128)"`.

### `error: can't connect to X server!` from `vainfo` — (reproduced)

`vainfo` looks for X11 by default. On a headless host or in a container, ask it
the question you meant: `vainfo --display drm --device /dev/dri/renderD128`.

### The encoder probes clean but the stream falls behind realtime

Not a detection problem. Check `GET /api/v1/encoders` for the probe duration —
a hardware encoder that takes seconds to open a single frame is usually a
driver quietly falling back to software. Otherwise the encode is simply beyond
the part: rendition *down* from 4K rather than re-encoding at 4K, which is what
renditions are for.

---

## Why not one image

It is a fair question, and the answer is that the alternatives are worse.

A single image would have to carry the CUDA base layers, the Intel media
driver, Mesa and the VA-API stack — hundreds of megabytes that every CPU-only
user downloads and never opens — and it still could not do NVENC, because the
part NVENC actually needs is on the host and arrives at `docker run`. The
default image staying small and boring is worth more than the convenience of
one tag.

datarhei Restreamer ships per-vendor images for the same reason. This is not a
workaround; it is the shape of the problem.

---

## What has actually been run, and what has not

Everything on this page was written on an **Apple Silicon macOS machine with no
discrete GPU**, with Linux behaviour staged in containers on the same host.
Docker Desktop's VM exposes no `/dev/dri` and no NVIDIA device, so no GPU of any
kind was involved anywhere. This section is the honest boundary of that, because
a troubleshooting page that cannot tell a reproduced error from a reasoned one
is worth less than no page.

Entries marked **(reproduced)** in the troubleshooting section above were
produced by running the command and copying the output. Everything else was read
verbatim out of `libavcodec.so.60` / `libavutil.so.58` with `strings`, which
gives the exact wording but not the conditions that trigger it.

This is also why hardware encoding carries an **EXPERIMENTAL** label in the
product — but on the NVENC, QSV, VA-API and AMF rows only. Eight of the twelve
encoder profiles configured in 0.7.0 — the per-encoder flags in
[ENCODING.md](ENCODING.md#per-encoder-flags), and the capped-VBR fix on NVENC —
were derived from the same containers described above, so they have the same
status as everything else in this section: correct as far as FFmpeg's own
option tables go, unconfirmed on silicon. The label does not disable anything.

The other four are confirmed. `TestEveryConfiguredEncoderOpensWithItsOwnFlags`
runs a real encode per registered encoder with that encoder's own flags — the
capped-VBR path included — and on this Apple Silicon machine
`h264_videotoolbox`, `hevc_videotoolbox`, `libx264` and `libx265` all pass. The
test answers for whichever encoders the machine running it registers, which is
why a GPU-less container cannot retire the paragraph above and a runner with an
NVIDIA card could.

**Verified by running it:**

- The headline divergence. A `ubuntu:24.04` container with no GPU lists
  `h264_nvenc`, `hevc_nvenc`, `h264_qsv`, `hevc_qsv`, `h264_vaapi`, `hevc_vaapi`
  and `libx264`, and only `libx264` encodes a frame. This is the bug, end to end.
- The probe on this macOS box: `h264_videotoolbox` and `libx264` both encode;
  nothing else exists to try.
- That a rendition on a listed-but-unusable encoder is refused with FFmpeg's own
  message and does not crash-loop, and that a `libx264` rendition beside it on
  the same FFmpeg still runs. Both are asserted by
  `scripts/acceptance-encoders.sh`, using a shim FFmpeg that lists `h264_nvenc`
  and fails to encode with it.
- That the server still starts, still offers every encoder and still renders a
  correct 720p file when every detection command fails.
- The runtime layer of `Dockerfile.cuda` and `Dockerfile.vaapi` (apt pins
  resolve, drivers present, `vainfo` present, non-root user works), and
  `docker compose config` on every GPU block in `docker-compose.yml`.

**Never run against real hardware — treat as reasoned, not demonstrated:**

- **NVENC.** No NVIDIA GPU, no driver, no `nvidia-container-toolkit`. `--gpus
  all` has never been exercised, a successful `h264_nvenc` encode has never been
  observed, and the claim that `NVIDIA_DRIVER_CAPABILITIES=...,video` is what
  causes `libnvidia-encode.so.1` to be injected comes from the toolkit's
  specification plus the verified base-image `env`, not from watching it happen.
- **Quick Sync** on real Intel silicon, and **AMF** on real AMD. AMF's absence
  from Ubuntu's FFmpeg is verified; everything else about it is not.
- **VA-API against a real render node.** `--device /dev/dri:/dev/dri`,
  `--group-add` with a real GID, and a successful `h264_vaapi` encode are all
  untested. In particular the `Permission denied` entry — the single most common
  real-world failure — could not be staged, because staging it needs a render
  node with real permissions.
- **Multi-GPU render node selection, on real multi-GPU hardware.** The wiring
  gap here is closed: detection enumerates `/dev/dri`, keeps only usable render
  nodes, ranks Intel and AMD above an NVIDIA node (which opens fine but has no
  VA-API encode entrypoint without the shim driver), and **the probe now names
  the node detection chose** rather than `renderD128` regardless. That is
  covered by unit tests over the argument builder.
  What remains untested is the thing those tests cannot reach: a host with two
  GPUs where the first render node is genuinely display-only. The selection
  logic is verified against synthetic device lists, never against silicon that
  disagrees with them.
- **Windows**, and therefore AMF's primary platform, entirely.
- **A full `docker build`** of either GPU image. Only the runtime stage was
  built; stages 1 and 2 are byte-identical to the default `Dockerfile`. Only
  `linux/amd64` was checked — the FFmpeg apt pin was not confirmed on `arm64`.

If you run polyemesis on any of the above, `Renditions → re-detect hardware`
prints exactly what your machine said, and that output is worth more than this
page.
