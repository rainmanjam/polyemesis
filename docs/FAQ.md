# FAQ

## What is this for, exactly?

Ingest one stream, send it to many places, and give **each destination its own
audio mix**. Your main channel gets mic + music, the second-language stream gets
mic + commentary, the podcast feed gets mic only — from one OBS, one upload.

That last part is the differentiator. Plenty of things will fan a stream out.
Very few will give each destination a different mix of the incoming tracks.

## How is it different from datarhei/Restreamer or restream.io?

**Per-destination multichannel audio routing.** Restreamer fans out and
transcodes but treats audio as one thing. restream.io is a hosted service and
takes a cut of your reliability and your data.

polyemesis also never re-encodes video on a destination path, which is why a
dozen destinations cost roughly what one does.

## Does it re-encode my video?

**No, and this is not negotiable.** Destinations use `-c:v copy`. Whatever your
encoder produces is exactly what each platform receives, bit for bit.

Renditions are the exception, and they are opt-in: if you explicitly want a
720p ladder alongside your 1080p, that rung is encoded because it has to be.

## How many destinations can I run?

Bounded by upload bandwidth, not CPU. Copying video is nearly free; the cost per
destination is one audio re-encode, which is a rounding error.

Ten destinations on a small VPS is unremarkable. Ten destinations' worth of
upstream bandwidth is the real constraint.

## Do I need SRT? Can I use RTMP?

You can use RTMP, but **RTMP carries one stereo pair**. If you only have one
audio track there is nothing to route, and the main reason to use polyemesis
disappears.

For multitrack you need SRT. Check with:

```sh
ffmpeg -protocols | tr ' ' '\n' | grep -x srt
```

Homebrew's FFmpeg on macOS has no SRT. Use Docker.

## What about Enhanced RTMP / multitrack FLV from OBS 30.2+?

Not implemented. The `enhancedRtmp` config key exists so old config files keep
parsing, but **nothing branches on it** and RTMP ingest is single-track either
way. Use SRT.

## Can I run horizontal and vertical at once?

Yes — that is what multi-source is for. Add a second source, point OBS's vertical
plugin at it, and give each its own destinations. They are independent all the
way down: separate ingest, routing, renditions and recordings.

They share one SRT port and are told apart by their publish tokens — there are
no per-source ports to allocate or publish.

## What happens if my encoder drops?

With failover off (the default): destinations stop receiving and the platforms
see a stall.

With failover on: a standby slate goes out instead, built at the *probed*
geometry of the departed ingest so a copying destination does not choke on the
change. Your destinations never restart, so the platform connections survive.
When the encoder returns, it goes back automatically.

## Why does my video-only stream get refused?

Every major platform refuses video with no audio. Turn on the silence tier
(**Settings → Synthetic**) and polyemesis synthesises a silent stereo track.

## Is there a multi-user mode?

No. One admin, no roles, no per-destination permissions. **Access to the UI is
full control of the server's streaming** — and, via expert mode and file
destinations, meaningful control of the machine.

Put a reverse proxy in front if you need access control.

## Can I expose it to the internet?

Only with TLS. `tls.mode: auto` is the one-line fix; binding to `127.0.0.1` and
using an SSH tunnel is the zero-configuration alternative.

Read [SECURITY.md](../SECURITY.md) before you do — particularly the section on
what it deliberately does *not* defend.

## Where is my data?

All under `<dataDir>` (default `./data`): the database, `secret.key`,
recordings, HLS segments, TLS material. Nothing is written outside it.

**Back it up, and treat it as secret.** `secret.key` decrypts your stored
platform tokens.

## Can I configure destinations in a file?

No, and deliberately. `config.yaml` holds only what must be known before the
database opens. Everything else is runtime state edited in the UI — a config
file for it would be a second source of truth.

For automation, use the [API](API.md).

## Does it work on a Raspberry Pi / NAS?

It should — one static binary, no cgo, arm64 images published. Copying video is
cheap enough that modest hardware is fine.

The caveat is FFmpeg: check the version and SRT support. On a NAS, Docker is
usually the least painful route.

## Is Windows supported?

**Verified, and unproven in operation** — the same two words the
[platform table](../README.md#platform-maturity) uses.

Verified: every push runs the full Go suite on `windows-latest` with FFmpeg
installed, then pushes a three-track broadcast through the binary and measures
per-band energy in each destination's output. That job has already caught real
Windows-only bugs — a `file://` URL corrupted by path separators, TLS keys
written world-readable because `os.FileMode` is a no-op there, and path
traversal reaching four resolvers through `/`.

Unproven: nobody *operates* it on Windows. No live broadcast has gone to a real
platform from a Windows host, the Service Control Manager wrapper and installer
scripts have never run anywhere but a developer's imagination, and recording
truncation on service stop is a known unresolved problem.

Linux and Docker are the primary targets.

## Why is HSTS off by default?

Because a browser remembers it. Once it has seen the header for a hostname it
refuses plain HTTP to that name until `max-age` elapses, and the server cannot
take it back. If your certificate is later lost or the instance moves, you are
locked out of your own tool.

Turn it on when you have a publicly trusted certificate and intend to keep one.

## Can I use my own FFmpeg build?

Yes: `ffmpeg.binary` and `ffmpeg.probe` in the config, or `-ffmpeg` / `-ffprobe`
flags. It must be 6.0+, and needs SRT for multitrack ingest.

## What does "polyemesis" mean?

Loosely, "many sendings-forth". One stream in, many out.

## Where do I report a bug or ask for a feature?

Issues, with the templates. Security problems go through
[SECURITY.md](../SECURITY.md) instead — please not a public issue.
