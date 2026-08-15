# Keyword research — measured, August 2026

DataForSEO. Google US. `search_volume` is monthly; **KD** is Keyword Difficulty,
0–100 log scale, the chance of reaching the organic top 10.

Method note: `keyword_ideas` was tried and discarded — it matches by product
*category*, so "obs audio tracks" returned car audio, DNS servers and audio
bibles. `keyword_suggestions` requires the seed to appear in the phrase and is
the right instrument for a niche this specific. `search_volume` caps at **10
keywords per call**.

## Tier 1 — the multistream cluster

Where the demand is, and it is almost entirely untended.

| Keyword | Vol | KD | CPC |
|---|---|---|---|
| **aitum multistream** | 3,600 | **4** | $8.18 |
| multistream | 3,600 | 35 | $6.65 |
| twitch multistream | 1,000 | 27 | $4.78 |
| **multistream obs** | 880 | **3** | $7.04 |
| how to multistream on obs | 720 | 29 | $9.34 |
| **aitum multistream obs plugin** | 720 | **4** | $9.99 |
| obs multi rtmp plugin | 720 | — | $8.15 |
| **obs multistream** | 590 | **10** | $7.25 |
| obs-multi-rtmp | 590 | — | $8.42 |
| streamelements multistream | 590 | 15 | $4.13 |
| **restream multistreaming** | 590 | **5** | $1.30 |
| **how to multistream** | 480 | **2** | $6.18 |
| multi rtmp obs | 390 | — | $7.27 |
| aitum multistream plugin | 320 | 5 | $8.58 |
| multistream obs plugin | 260 | 19 | $8.42 |
| sorayuki multistream | 140 | 9 | — |
| multistream youtube | 90 | 2 | **$22.54** |

**A competitor's brand term is the single best target on this list.**
`aitum multistream` carries 3,600/mo at KD 4 — the same volume as the generic
head term at a tenth of the difficulty, because nobody has written anything
comparing it. The site's `/comparison` page already argues this exact case and
does not rank. `obs-multi-rtmp` and `streamelements multistream` are the same
shape.

## Tier 2 — the restream cluster

| Keyword | Vol | KD | CPC |
|---|---|---|---|
| restream | 60,500 | 16 | $1.63 |
| **free restream service** | **14,800** | **22** | $2.13 |
| restream io | 8,100 | 14 | $2.46 |
| restream studio | 2,400 | 28 | $3.75 |
| restream pricing | 590 | 20 | $3.02 |
| is restream free | 260 | 15 | **$13.01** |
| what is restream | 260 | 12 | $10.97 |
| restream alternatives | 210 | — | $8.03 |
| restream alternative | 210 | — | $7.43 |

**`free restream service` — 14,800/mo at KD 22 — is the largest addressable term
found anywhere in this research**, and it describes polyemesis literally: free,
MIT, self-hosted, no account. The pricing-and-free-tier queries beneath it
(`is restream free` at $13.01 CPC, `restream pricing`, `what is restream`) are
the same buyer a step earlier, and agy's competitor pass established what
restream.io's free tier actually gates: 2 channels, no custom RTMP, no Facebook
Pages, 720p and a watermark in Studio.

## Tier 3 — technical, small but qualified

| Keyword | Vol | KD | CPC |
|---|---|---|---|
| twitch vod track obs · twitch vod track | 110 each | — | — |
| srt server | 90 | 10 | $2.26 |
| obs twitch vod track | 70 | — | — |
| obs srt | 50 | — | — |
| srt live server / srt-live-server | 40 each | 7 | **$11.30** |
| obs vod track | 30 | — | — |

## Confirmed dead — do not target

The `obs audio track` seed returned **two** keywords with any volume at all
(`audio track obs` 50, `obs only recording one audio track` 30). Combined with
the earlier no-data result across every problem-language phrase
(`twitch vod muted music`, `obs different audio per platform`, `stream different
audio to youtube and twitch`, `per destination audio routing`), the conclusion
is firm:

**The product's differentiator has no search demand. The category does.**

That is not an argument for changing the product's positioning — it is an
argument about acquisition. People search for `multistream`, discover the tools,
and only then care whose audio model is better. The differentiator wins the
comparison; the category term has to win the click first.

## Competitor weakness, measured

`docs.datarhei.com` — Restreamer's documentation, the closest self-hosted
analogue — ranks for **26** keywords above 30/mo, at positions **23 to 87**, and
almost all of them are generic protocol terms it ranks for by accident:
`rtmp port`, `rtsp url format`, `port 554 rtsp`, `obs srt` (position 46).

It ranks for essentially nothing about what it *is*. The self-hosted side of
this category has no incumbent holding the terms that matter.

## What this implies

1. **Comparison pages against named tools**, starting with Aitum Multistream and
   obs-multi-rtmp. Highest volume-to-difficulty ratio available, and the argument
   is already written — it just has no page targeting the query.
2. **A "free restream service" page**, honest about what self-hosting costs
   (a box, a domain, FFmpeg) as well as what it removes (per-channel limits,
   watermarks, a monthly bill).
3. **`how to multistream` / `multistream obs` as tutorial content** — KD 2 and 3,
   and the docs rendering already in flight supplies most of the material.
4. Leave the audio-routing vocabulary exactly where it is: on the pages people
   reach *after* the category term brought them.

---

# The changes this research argues for

Volume figures are the sum of the cluster each page would target, not a single
keyword. KD is the range across that cluster.

| # | Change | Targets | Vol/mo | KD | Effort |
|---|---|---|---|---|---|
| 1 | **New `/vs/aitum-multistream`** | aitum multistream · + obs plugin · + plugin | **4,640** | **4–5** | M |
| 2 | **New `/free-restream-service`** | free restream service · is restream free · restream pricing · what is restream | **16,250** | 12–22 | M |
| 3 | **New `/vs/obs-multi-rtmp`** | obs multi rtmp plugin · obs-multi-rtmp · multi rtmp obs · multistream obs plugin | **2,550** | 19 | M |
| 4 | **New `/how-to-multistream-from-obs`** | multistream obs · how to multistream on obs · obs multistream · how to multistream | **2,670** | **2–29** | M |
| 5 | **New `/vs/restreamer`** | restream alternative(s) · restreamer alternative | 630 | — | S |
| 6 | Rework `/comparison` into a hub linking 1/3/5 | multistream · twitch multistream | 4,600 | 27–35 | S |
| 7 | New `/vs/streamelements` | streamelements multistream (+ obs plugin) | 680 | 15 | S |
| 8 | Fold `srt server` / `srt live server` into a docs page | srt server · srt-live-server · obs srt | 180 | 7–10 | XS |
| 9 | ~~Target the audio-routing vocabulary~~ | — | **~80** | — | **Don't** |
| 10 | ~~Chase the `restream` head term~~ | restream (60,500) | — | 16 | **Don't** |

## Why the two rejections

**#9.** Every problem-language phrase returns no data, and the `obs audio track`
seed yields two keywords totalling 80/mo. The differentiator is what wins the
comparison once someone is reading; it is not what brings them.

**#10.** `restream` is 60,500/mo at KD 16, which looks irresistible and is a
trap: it is a navigational brand query. Someone typing it wants restream.io's
login page. `free restream service` is the same audience with intent we can
actually serve, at a quarter of the volume and none of the futility.

## The constraint on 1, 3, 5 and 7

These pages name competitors, so every claim on them is a legal and
reputational exposure, not a copy choice. Two things must be right.

**The audio claim must be precise.** obs-multi-rtmp and Aitum **select** an OBS
audio track per destination — and the *summing* that produces those tracks
happens upstream in OBS's Advanced Audio Properties. So "they cannot send
different audio per destination" is FALSE and must never be written. What is
true, and is the actual argument:

> They select a track OBS has already mixed. Building three different mixes means
> configuring three track layouts in OBS and encoding each output on the stream
> PC. polyemesis sums the tracks server-side, so one upload and one video encode
> feed every destination.

**Pricing claims decay.** restream.io's tiers move. Anything quoted needs a
"checked on" date next to it and a periodic recheck, or it becomes a false
statement about a competitor's prices — which is a different class of problem
from a stale docs page.

`docs/COPY-CONSTRAINTS.md` governs all of this and should be re-read before any
of these pages is written.
