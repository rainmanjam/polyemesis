# Testing against real platforms: what is needed, and what it costs

Parked 2026-08-15. `scripts/acceptance-multistream.sh` is the only suite that
publishes to real streaming platforms, it has found two defects nothing else
could, and it currently **runs only when somebody remembers to run it**. This
note is what to do when picking that up.

## What it already proves

Four platforms, real credentials, 44 checks:

| Platform | Ingest | Key persistence |
|---|---|---|
| Twitch | `rtmp://live.twitch.tv/app` | persistent |
| YouTube | `rtmp://a.rtmp.youtube.com/live2` | persistent |
| Kick | per-channel, no default | via API, may rotate |
| Facebook | `rtmps://live-api-s.facebook.com:443/rtmp` | **per broadcast — see below** |

Each platform is independent: a missing key means that platform is **skipped and
counted as skipped**, never silently passed, and `EXPECTED_CHECKS` cannot be
satisfied by skipping. So one platform is a valid starting point.

On its first live runs this suite found a stream key written to `server.log` on
every retry (#310) and a shipped Kick preset that could not publish (#312).
Neither was reachable offline: on each side of the boundary both halves were
individually correct, and only a real far end refuses a bad composition.

## What it deliberately does NOT prove

From its own header:

> Confirming what a platform received needs that platform's own playback or API
> and is out of scope here.

So it proves *the platform accepted and sustained our publish* — not *what the
platform played back*. That is the "four green cards, four healthy bitrates, and
nothing actually verified" trap the file names.

Step 4b exists to close that from the other side: one destination publishes into
**polyemesis's own RTMP ingest**, where the media is decoded and the mix
measured. Real platforms give ground truth on credentials and acceptance; the
loopback gives ground truth on content.

## Environment variables

```
TWITCH_STREAM_KEY      YOUTUBE_STREAM_KEY      KICK_STREAM_KEY      FACEBOOK_STREAM_KEY
TWITCH_INGEST_URL      YOUTUBE_INGEST_URL      KICK_INGEST_URL      FACEBOOK_INGEST_URL
```

Only the keys are required. All ingest URLs have defaults **except Kick's**,
which is per-channel and must be supplied whenever `KICK_STREAM_KEY` is set.

**Environment only. Never an argument, never a file the suite reads, never a
prompt.** `ps(1)` is world-readable, so a key on a command line is disclosed to
every local user for as long as the process runs. The driver reads them with
`os.Getenv` and POSTs them over loopback, and steps 8a–8d then *verify* the
value did not escape into a log, an artifact, or a command line.

## Two decisions to make before wiring a cron

**Facebook will not work as a static secret.** `docs/PLATFORMS.md` records that
Facebook issues a fresh ingest URL and key per broadcast — connecting the
account is what creates the broadcast, and there is no permanent key to reuse. A
stored `FACEBOOK_STREAM_KEY` goes stale after its first use. Kick's is fetchable
with `streamkey:read` and may rotate. **Twitch and YouTube are the two that
survive as secrets.**

**A cron makes the channel briefly go live, for real.** Followers are notified,
a VOD is created, analytics are polluted. Point it at throwaway or secondary
channels, or accept a weekly blip on the real ones.

## Two ways to do it, in the order worth doing them

### 1. By hand, first — verifies the build today

```bash
export TWITCH_STREAM_KEY=...   # in your own shell, not in a command that gets logged
export YOUTUBE_STREAM_KEY=...
./scripts/acceptance-multistream.sh
```

This is the better first step: it answers "does the shipping build still fan out
correctly" before any secrets are committed anywhere, and it tells you whether
the cron is worth wiring at all.

### 2. Then a cron, if the by-hand run is clean

```bash
gh secret set TWITCH_STREAM_KEY  --repo rainmanjam/polyemesis
gh secret set YOUTUBE_STREAM_KEY --repo rainmanjam/polyemesis
```

`gh secret set` prompts for the value so it never enters shell history. Then a
workflow shaped like `chat-live.yml` / `oauth-live.yml`: weekly, on its own cron
minute, passing the keys through `env:` and skipping cleanly for whatever is
absent.

The argument for a clock rather than a push is the same one those workflows
already make: **these measure something we do not control**, so a failure
arrives with no commit of ours attached. A push cannot catch a platform changing
its API.

## Status when this was parked

- No platform keys configured, in the repo or on the maintainer's machine.
- Repo secrets present: `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN`,
  `DOCKERHUB_TOKEN`, `DOCKERHUB_USERNAME` — all publishing, none streaming.
- The suite is wired to no workflow.

## Related gaps, parked with this one

- **No broadcast has been published through a minted Twitch key.** Enhanced
  Broadcasting *negotiates* successfully against `ingest.twitch.tv` on every
  test run; everything after `Negotiate` returns has only been driven by a
  stand-in server. Needs a supported GPU declared in settings plus a real key.
- **No NVENC, QSV, VA-API or AMF encode has been observed.** All twelve encoder
  profiles were read off real FFmpeg option tables; only VideoToolbox has run on
  silicon. A GPU host closes this and the item above together.
- **Playback verification** — YouTube Data API `liveStreams.status`, Twitch Helix
  `/streams`, Kick's channel endpoint. Confirms the platform thinks we are live,
  rather than that our socket stayed open. Lowest priority: the loopback far end
  already covers content more rigorously than a status field would.
