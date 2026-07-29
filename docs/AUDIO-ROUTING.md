# Audio routing

This is the feature the rest of the product exists to serve.

One encoder sends up to six audio tracks. Every destination gets its own mix of
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

## Mix matrix

A grid mapping every channel of every track onto L and R, with a gain per cell
from 0.0 to 2.0.

This subsumes simple mode and additionally lets you take only the rear channels
of a 5.1 track, pan a mono mic hard left, or swap channels.

## Clip protection

Summing tracks can exceed full scale. The options:

| Mode | Behaviour |
|---|---|
| `auto` (default) | A limiter is inserted whenever two or more tracks are combined, and omitted for a single track |
| `off` | No limiter. You are responsible for the gain staging |
| `limiter` | Always inserted |
| `loudnorm` | EBU R128 loudness normalization to −16 LUFS instead of a limiter |

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
  platform on the other end actually receives.
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
- [ARCHITECTURE.md](ARCHITECTURE.md#3-audio-routing-engine-internalrouting) — the
  routing engine's model and compilation
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md#the-audio-is-wrong) — when the audio is
  wrong
