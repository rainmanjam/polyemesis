# Audio routing

This is the feature the rest of the product exists to serve.

One encoder sends up to 32 audio tracks — six is what OBS sends, and what most
setups use. Every destination gets its own mix of
them, compiled into its own FFmpeg `-filter_complex` graph. YouTube can receive
the clean mix while your archive keeps everything and your Discord restream
keeps the mic hot — from one upload and one video encode.

Every destination has its own routing profile, and changing one restarts **only
that destination**. The ingest, the recorder, and every other destination are
untouched.

---

## Simple mode

A row per ingest track with a checkbox and a gain slider. Ticked tracks are
summed into that destination's stereo output.

Tracks with more than two channels are stereo-downmixed first, using FFmpeg's
normalized ITU coefficients — a 5.1 track folds down with LFE dropped, scaled so
that a fully correlated source cannot clip.

Which channel is which comes from the layout ffprobe reports, not from the
channel count: a 2.1 track is FL FR LFE and a 3.0 track is FL FR FC, and only
one of those has a centre channel to fold in. A layout polyemesis does not
recognise falls back to FFmpeg's native ordering for that width, and a width
with no canonical ordering is split even-channels-left, odd-channels-right at
matched gain so nothing is dropped and the image stays centred.

## Mix matrix

A grid mapping every channel of every track onto L and R, with a gain per cell
from 0.0 to 2.0.

This subsumes simple mode and additionally lets you take only the rear channels
of a 5.1 track, pan a mono mic hard left, or swap channels.

## Two mixes to one destination

> **EXPERIMENTAL — on Twitch, no broadcast has ever been published through a key
> Enhanced Broadcasting minted.** The negotiation itself is not the gap: it runs
> against `ingest.twitch.tv` and succeeds, and polyemesis's own tests reach the
> live endpoint on every run — Twitch accepts a supported-GPU inventory, grants
> a VOD audio track and mints a key. What has never been observed is everything
> *after* that: no second audio track has been seen arriving at Twitch, and the
> engine wiring that carries the decision has only been driven by a stand-in
> server. It stays fully enabled and there is no flag to turn it off — a
> negotiation that does not succeed falls back to the ordinary Twitch ingest,
> which is the path every install used before this existed. The generic two-mix
> egress on a **non-Twitch** destination is a separate mechanism and is not
> covered by this caveat.

A destination can receive a second audio track carrying a different mix. The
live broadcast keeps the music bed; the archive track does not — from one
ingest, one video encode, one connection.

Today that means **Twitch Enhanced Broadcasting**, which is the only published
ingest that takes two audio tracks and says what the second is for. polyemesis
can emit a second track to any RTMP destination and its own RTMP server receives
it as two distinct tracks, but no other platform has been measured accepting
one, and the `-c:a copy` refusal for RTMP destinations still stands.

### Turning it on

In the UI: **Routing → pick the destination → Second (VOD) audio mix**. The
switch there is off on every destination and switching it on opens a second copy
of the same editor the live mix uses — the same track picks, mix matrix, music
rights, loudness, delay and ducking, compiled and shown as its own filter graph.
It is seeded from the live mix, so the first edit you make is the actual
difference you wanted.

Over the API, two fields on the destination, both off by default:

| Field | What it does |
|---|---|
| `multitrack` | Opts into the Enhanced Broadcasting negotiation with Twitch. |
| `vodProfile` | The second mix. A full routing profile — its own track selection, its own normalization, its own sample rate. |

`vodProfile` is a complete profile rather than a track list, so the archive mix
is built the same way the live one is: pick tracks in simple mode, or open the
mix matrix and set gains per channel. The two are independent. Muting the music
track in the VOD profile is the common case, and it is the only difference most
setups need.

With `vodProfile` unset the destination produces byte-for-byte the filter graph
and the command line it produced before this existed.

### On Twitch, `vodProfile` needs `multitrack`

The ordinary Twitch RTMP ingest takes one audio track. Enhanced Broadcasting is
what takes two.

Nothing enforces the pairing. Setting `vodProfile` without `multitrack` is
reported rather than corrected, because a setting that silently undoes itself is
worse than one that explains itself — if the engine quietly cleared the VOD
profile, the operator's evidence would be an archive track that never appears
and a form that still says it should.

### It needs a GPU

Twitch's negotiation endpoint requires GPU information in the request and
refuses a client that reports none:

> Your broadcast software (polyemesis) did not send GPU Information which is
> required by GetClientConfiguration

The GPU is not doing the work — video is still copied and the second track is an
audio encode. It is a precondition Twitch checks. A headless server with no GPU
cannot negotiate Enhanced Broadcasting however much CPU it has, and the
destination falls back to the ordinary ingest and says so once.

Multitrack **video** is not required. A single video rendition is enough:
`maximum_video_tracks: 1` returns one rendition and both audio tracks, which is
what makes this reachable at all. The feature is named for multitrack video and
does not need it.

### What happens when the second mix will not compile

A secondary that cannot be built is a **warning**, not an error. An optional
archive track must never veto a working broadcast, so the destination goes live
with its live mix and reports that the second one did not build.

A primary that will not compile is still an error. There is no stream without
it.

### The live mix is not rewritten

The primary is compiled in the empty namespace, so the first track's filter
graph is byte-identical to what it would be with no second mix at all. That is
asserted rather than assumed, by
`TestTheEmptyNamespaceIsByteIdenticalToTheSingleMixGraph` — "ticking the VOD box
does not change what my viewers hear" is the promise an operator is actually
relying on, and a promise nothing checks is one that decays.

### What it costs

A second AAC encode. The picture is still copied, not re-encoded, so the video
cost is unchanged — but two mixes are two audio encodes, and the "0 re-encodes"
figure has always meant the video. Two taps of the same ingest track need no
`asplit`; both halves read the same input pad directly.

## Clip protection

Summing tracks can exceed full scale. The options:

| Mode | Behaviour |
|---|---|
| `auto` (default) | A limiter is inserted whenever two or more tracks are combined, or whenever any one output channel's coefficients total more than unity. Omitted for a single track that cannot reach full scale |
| `off` | No limiter. You are responsible for the gain staging |
| `limiter` | Always inserted |
| `loudnorm` | EBU R128 loudness normalization instead of a limiter |

The second clause matters because `pan` sums too. Validation caps each cell at
2.0, never the row, so a single track with three cells at 2.0 on one leg reaches
six times full scale — and `auto` used to reason that one track cannot clip and
insert nothing.

**A loudness target changes what `auto` does.** Naming a target on the
destination is itself a request for loudness normalization, so `auto` arms
`loudnorm` at that target rather than the limiter — including for a single
track, where it would otherwise insert nothing. `off` and `limiter` are explicit
choices and are never overridden; a target set alongside either is ignored for
this stage.

Without a target, `loudnorm` runs at −16 LUFS, the figure the streaming
platforms expect.

> polyemesis sets `amix=normalize=0` deliberately. FFmpeg's default divides the
> sum by the number of inputs, which quietly drops a three-track mix by about
> 9.5 dB — a bug that shipped here once and was found by measuring output RMS,
> not by reading the filtergraph. Levels are controlled by per-track gain
> instead, and the resulting clip risk is what the limiter is for.

## Presets

**Everything** · **No music** (all tracks except a nominated one) · **Mic only**
· **5.1 → stereo**.

The 5.1 preset is emitted as an editable matrix rather than an opaque mode, so
you can see the coefficients it chose and change them.

## Loudness and timing

Per destination, independent of the mix:

- **Loudness target** — EBU R128, measured *after* routing, which is what the
  platform on the other end actually receives. It is single-pass, because a live
  programme is not available to measure in advance: it adapts as it goes and
  converges over roughly the first minute, so an early reading is not yet the
  number you will deliver. Setting one also arms `loudnorm` as the clip stage —
  see above.
- **Ducking** — attenuate one track group while another is active.
- **Audio delay** — positive or negative, in milliseconds.

A negative audio delay is implemented with the `setts` bitstream filter on the
video stream rather than `-itsoffset` on the input. `-itsoffset` shifts every
stream of an input together, so audio and video move in lockstep and the
delivered offset measures 0 ms for every requested value. `setts` moves video
alone and preserves `-c:v copy`.

## Transparency

The routing editor shows the **exact `-filter_complex` string** that will be
passed to FFmpeg, recompiled live as you edit, by the same code that runs it.

Copy it and reproduce any destination by hand. This is deliberate: an audio
graph you cannot inspect is one you cannot debug, and the difference between a
clean track and a not-quite-clean one is not something you can hear reliably at
2 a.m.

---

## Verifying it before you go live

Open **Audio meters** and confirm each track carries what you think it carries.
The acceptance suite does the machine version of this — routing a distinct tone
into each track and measuring the RMS of each destination's output through a
bandpass filter — because asserting that the compiler returned no error proves
nothing about what came out.

See [TESTING.md](TESTING.md) for how to run it.

---

## See also

- [OBS.md](OBS.md#2-assign-sources-to-tracks) — assigning sources to tracks in OBS
- [RENDITIONS.md](RENDITIONS.md) — shared video encodes, which never touch audio
- [ARCHITECTURE.md](ARCHITECTURE.md#2-audio-routing-engine-internalrouting) — the
  routing engine's model and compilation
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md#the-audio-is-wrong) — when the audio is
  wrong
