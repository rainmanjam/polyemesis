# Website copy review — 2026-08-08

Reviewed `web/src/` against the code and docs after the 0.5.0 cycle and PRs
#129–#131. Claims were checked against the repository, not taken on trust.

## Verified correct — no action

| Claim | Where | Checked against |
|---|---|---|
| "up to 32 tracks" | `SpecStrip`, index hero | `internal/routing/profile.go:27` `MaxTracks = 32` |
| "NVENC, QSV, VA-API, VideoToolbox, AMF" | comparison | all five present in `internal/` |
| "one audio track on any FFmpeg below 7.1" | download | `docs/CONFIGURATION.md:113`, `docs/FAQ.md:58` |
| "Ubuntu 24.04's stock FFmpeg 6.1.1" | download | `docs/INSTALL.md:305` |
| −15.6 / −17.0 / −21.9 LUFS | index body + `alt` | body and alt text agree |
| MIT licence | footer, download | `LICENSE` |

The Windows caveat on the download page ("what is not yet proven on Windows is
long-running service operation") is unusually honest for marketing copy and
should be left exactly as it is. It is also the claim this session's CI
failures independently vindicated.

## D1 — "There is no account recovery" is now false

`web/src/pages/download.astro:102`.

`-reset-admin` shipped this cycle and is documented in `docs/FAQ.md` and
`docs/CONFIGURATION.md`. The website was never updated, so it now tells an
operator the opposite of what the product does — and it says it at exactly the
moment they are choosing a password, which is when the statement carries the
most weight. Someone who believes it and loses the password will reinstall.

Suggested:

> Open the UI and set an admin password. If it is ever lost, `-reset-admin` on
> the box will set a new one — there is no recovery from anywhere else.

## D2 — the comparison table exists twice and has already drifted

`web/src/pages/features.astro` and `web/src/pages/comparison.astro` carry the
same eight-row "what only polyemesis does" table as two independent literals.
They already disagree on the last row:

- features: "Runs on your hardware, no per-destination pricing"
- comparison: "Runs on hardware you control, no per-destination pricing"

Both derive from `docs/COMPARISON.md`. One shared data module imported by both
pages removes the drift permanently; the alternative is that these two tables
diverge a row at a time and nobody notices which is current.

## D3 — unlinked competitive claim

`comparison.astro`: "'Multiple audio tracks for Twitch' is an open request on
Restreamer's own tracker."

Sourced internally from `docs/RESEARCH-COMPETITIVE.md`, but the page presents it
as a current fact about a third party with no link and no date. It is the one
line on the site that asserts something about someone else's project, so it is
the one that should be checkable. Link the issue and date the observation, or
cut it — the surrounding argument does not depend on it.

Related: "restream.io — 2–8 by plan" is a pricing claim that rots silently.
Date it or link it.

## D4 — near-duplicate chat copy with tense drift

- index: "a moderator card showing what one person **said** across every platform"
- features: "a moderator card showing what one person **has said** across every platform"

Same sentence, two tenses. Pick one; "has said" reads better next to "at once".

## D5 — the comparison page has four different names

| Surface | Label |
|---|---|
| Nav | Comparison |
| Footer | How it compares |
| features.astro heading | How it compares |
| index link | The full comparison → |

Nav and footer disagreeing on the name of the same page is the one that matters;
a reader who scanned the nav for "How it compares" will not find it.

## D6 — the hero and the problem section state the premise differently

- hero: "Every live platform accepts exactly one stereo pair."
- problem: "Every live platform accepts exactly one stereo audio stream."

Same claim, two phrasings, forty lines apart. "Stereo pair" is the better one —
it is the term an audio operator uses, and it is what the product actually
routes. Also consider "every major live platform": the absolute is doing no
extra work and is the kind of claim one counterexample embarrasses.

## D7 — nothing on the site mentions in-place upgrade

`#127`/`#129` shipped the update banner and the upgrade path. The install page
still reads as though upgrading means re-running the installer. Not wrong yet —
the endpoint is not wired — but it is the next copy change due, and it belongs
with the 0.6.0 release rather than after it.

## Note on scope

This is a copy review. I did not review the visual changes from #130/#131 (the
pointer lens and the displacement warp) because those need looking at, not
reading — that check is still outstanding.

---

# Second pass — codex and agy

Both were run over the same copy with my findings excluded, to see what I
missed. Every claim below was checked against the repository before being
recorded; two of theirs did not survive that check and are listed as rejected.

## Factual errors in the copy

| # | File | Current | Problem | Suggested |
|---|---|---|---|---|
| D8 | `features.astro:58`, `MixMatrix.astro:34` | "The full multitrack archive, **unencoded**" / "everything, unencoded" | The archive is **stream-copied**, not raw. "Unencoded" claims uncompressed audio and video, which is not what is written to disk. The site uses the correct term everywhere else. | "stream-copied" |
| D9 | `index.astro:287` | "with **nothing leaving your network**" | The product's entire job is sending your stream to YouTube, Twitch and Kick. It is also the sentence a privacy-minded reader will quote back. | "with no hosted service and no telemetry — it makes only the connections you configure" |
| D10 | `Base.astro:59` (structured data) | "Video is copied, **never** re-encoded." | False whenever a rendition is used, and this one is sitewide JSON-LD rather than body copy, so it is the version machines read. `index.astro:286` already states the caveat correctly. | "Video is copied by default; optional shared renditions re-encode video." |
| D11 | `comparison.astro:35` | Simultaneous destinations: "**Unlimited**" | `docs/FAQ.md` bounds it by upload bandwidth, and this session's own measurements put a 4-core box at roughly 96 destinations. There is no configured cap, which is a different claim. | "No configured cap — bounded by bandwidth and CPU" |
| D12 | `docs.astro:68` | "so they **never drift from the code**" | The next sentence tells release users to read their own checkout instead, which concedes the links can differ from what they run. | "They are versioned with the code. These links track `main`; for a release, read the docs in that checkout." |
| D13 | `download.astro:31` | "The **only** runtime dependency is FFmpeg" | Whisper transcription needs an external `whisper.cpp` binary (`internal/config/config.go:112`), and the site advertises transcription on `features.astro:61`. | "The only runtime dependency is FFmpeg. Transcription additionally needs whisper.cpp." |
| D14 | `index.astro:288` | "**Every state** the server knows is on one screen" | Disproved by opening the app: routing, recordings, monitoring and settings are separate pages. | "Live ingest, destination and process state are on one screen" |
| D15 | `features.astro:61` | "a transcript arriving an hour later **costs nothing**" | It costs CPU; the point being made is that it costs nothing *live*. | "a transcript arriving an hour later costs only time" |
| D16 | `index.astro:71` | `docker run … -p 8080:8080 -p 6000:6000/udp` | Omits `-p 1935:1935` while the same page sells RTMP as the fallback. Anyone who runs the hero command and points an RTMP encoder at it gets connection refused. | add `-p 1935:1935` |

## Smaller items

| # | File | Item |
|---|---|---|
| D17 | `Base.astro:50` | `operatingSystem: "Linux, macOS, Windows, Docker"` — Docker is not an OS. Both reviewers flagged it independently. |
| D18 | `Base.astro:31,36` | `og:image` and `twitter:image` have no `:alt`. |
| D19 | `404.astro:5` | `description="That page does not exist."` — 25 characters, no brand context. |
| D20 | `Base.astro:71–72` | `jetbrains-mono.woff2` is used but not preloaded, unlike the other two faces. |
| D21 | `Nav.astro:15` | Comment says "six ingest tracks"; the mark draws five rects. The mark is meant to *be* the product, and the product's example is six tracks. |
| D22 | `index.astro:267` | "The full list →" — weak out of context in a screen-reader link list. Both reviewers flagged it. |
| D23 | all pages | Every `<title>` exceeds 60 characters and will truncate in search results. Worth shortening, but not with the suggested rewrites — they drop the terms the pages actually rank for. |

## Rejected — checked and wrong

- **"MixMatrix should not say 'stereo pair', outputs can be mono or 5.1."**
  `internal/routing/profile.go:171` sets `OutChannels = 2`. Output is always a
  stereo pair. The 5.1 on `features.astro:27` describes an *input* being
  downmixed, not an output. The copy is right.

- **"The `curl` line-continuation backslash breaks copy-paste."**
  Tested: `\` before a newline is consumed by the shell and the URL rejoins
  intact. The snippet works as written.

## Where the two reviewers agreed

Independently on D17 and D22 only. Everything else was found by one and not the
other, which is the argument for running both.
