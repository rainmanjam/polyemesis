# Copy review of the marketing site — three reviewers, 2026-08-06

Opus, codex and agy each read `web/src` in full and checked every factual claim
against the repository. Below is what they found, what was verified, what was
fixed, and — as importantly — what one of them got wrong.

Fifteen accuracy defects were confirmed and fixed. The single worst one was
introduced by me an hour before the review ran.

---

## The one that mattered most

**The "Proof, not a diagram" block misdescribed its own screenshot.**

`LoudnessBars.astro` was written from the hero's *illustrative* arrangement
rather than from `scripts/seed_demo.go`, which is the code that generates the
capture sitting beside it — while its own comment claimed it carried "the same
numbers as the screenshot beside this".

| | shipped | truth (`seed_demo.go:136-140`) |
|---|---|---|
| YouTube | `01 + 03` | tracks 0,1,2 — the **full** mix |
| Twitch | `02 + 03` | tracks 0,2 |
| third destination | "Archive", "all six" | **"Podcast — mic only"**, track 0 |

Four compounding problems:

1. "Archive" does not exist in the capture.
2. "all six" was wrong twice: the source has **three** tracks, and that
   destination receives one of them.
3. YouTube was labelled as receiving track 01 — the full mix *including
   copyrighted music*. The same page argues at length that YouTube must get the
   clean mix or you take a strike. The block captioned "Proof" showed the
   product doing the exact thing the product exists to prevent.
4. The arithmetic was impossible. "all six" was shown **6.3 LU quieter** than a
   two-track destination. A superset cannot be quieter than its subset, and that
   is the first thing a broadcast engineer checks.

Under the true sets — 3 tracks / 2 tracks / 1 track → −15.6 / −17.0 / −21.9 —
the ordering is exactly right. The defect was entirely in the labels.

Also corrected here: the target tick was drawn at −23 LUFS and captioned as "the
EBU R128 target". −23 is EBU R128 *broadcast* delivery; `docs/AUDIO-ROUTING.md:52`
says `loudnorm` defaults to **−16**, "the figure the streaming platforms expect".
Marking −23 put all three bars on the loud side of a line nobody in this
workflow aims at.

---

## Accuracy defects fixed

| # | Defect | Evidence |
|---|---|---|
| 1 | Loudness proof block (above) | `scripts/seed_demo.go:136-140` |
| 2 | "One six-track source" — the capture uses three | `capture-media.sh:134-136`; the page's own `alt` text already said three |
| 3 | Install command **cannot run**: `--tls auto` invalid, `\| sh` fails, no root | `install.sh:1101` takes `off\|selfsigned\|acme`; `#!/usr/bin/env bash` + `${__answer,,}`; `install.sh:226` |
| 4 | OBS steps described `Settings → Stream`, which has no multi-track selector | `docs/OBS.md:50-67` requires Output → Advanced → Recording → Custom Output (FFmpeg) |
| 5 | "Moderation across all four: delete, ban, timeout" | `internal/chat/facebook.go` implements `Delete` and `Hide` only; `Ban` is YouTube/Twitch/Kick |
| 6 | "One send box that fans out" | No `Send` on Facebook; `PLATFORMS.md:34` marks it Unverified |
| 7 | "it is short" about the installer | `scripts/install.sh` is **1,164 lines** |
| 8 | TLS description was `config.yaml`'s `tls.mode: auto`, not the installer's | `install.sh:1033-1035` defaults to selfsigned; acme needs both flags |
| 9 | First-run steps omitted the ingest-mode choice | `internal/db/settings.go` — `IngestUnset`; no URL exists until chosen |
| 10 | `<h1>` said six tracks, four lines above a spec strip saying 32 | `internal/routing/profile.go` — `MaxTracks = 32` |
| 11 | "Video is copied, never re-encoded" | Contradicted by the site's own next card: renditions "re-encode video only" |
| 12 | `/docs` claimed links "match the release you are running" | Every link targets `blob/main` |
| 13 | FFmpeg dependency line said RTMP is unconditionally single-track | Enhanced RTMP works on FFmpeg 7.1+, verified end to end |
| 14 | RTMP fallback card contradicted the corrected RTMP row two screens down | Same page |
| 15 | Features `<h1>` "happens to route audio" vs subhead "the reason it exists" | Self-contradiction |

## Documentation drift fixed

`README.md:89`, `docs/FAQ.md:51` and `docs/OBS.md:127` all still stated Enhanced
RTMP multitrack was "not supported" — and `OBS.md` **contradicted itself within
one page**, carrying a callout near the top saying a new enough FFmpeg carries it
and a section 115 lines down saying it does not. All three now carry the verified
account: works on FFmpeg 7.1+, does not on 6.1.1, unconfirmed with OBS as
publisher.

`docs/notes/enhanced-rtmp-multitrack.md` had itself drifted — it cited
`MaxTracks` as "a global cap of 6" at a line number that had moved.

---

## What was rejected, and why

**Instagram.** Codex called "Instagram Live cannot work" too absolute, arguing
`PLATFORMS.md` documents rare accounts that retain a Live Producer RTMP endpoint
and could use a Generic RTMPS destination. It does not: `PLATFORMS.md:39` reads
"Not possible" in every column, and `:131` says plainly "polyemesis cannot stream
here". The site matches its source. Left unchanged.

**Agy's OBS fix.** Agy correctly flagged the missing ingest-mode step, but its
proposed replacement kept `Settings → Stream → Custom, service SRT` — preserving
defect #4, which the other two caught. A fix that carries the bug forward is
worth noticing when weighing reviewers against each other.

**"every channel of every track".** Opus flagged this as over-claiming now that
`MaxMeterChannels = 64` is reachable at 32 tracks. True, but it cannot bite a
six-track OBS user, and the same phrasing is in `README.md`. Noted, not changed.

**Deliberately kept:** the dBFS-vs-LUFS derivation note in `MixMatrix.astro`
(the most credibility-building paragraph on the site), six tracks as the working
*illustration* throughout, the "what it does not do" list, the comparison page's
instruction to go use Restreamer, and the structured data's absence of
`aggregateRating`.

---

## Still open

`web/public/shots/04-meters.png` — the image under "Proof, not a diagram" — has
**"Ingest Offline"** in its header bar. `features.astro:4-8` states the dashboard
capture was deliberately withheld for exactly this defect; the policy was simply
never applied to the one image carrying the site's central proof. Needs a
recapture, not a copy change.
