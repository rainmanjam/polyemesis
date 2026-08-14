# Website copy to add, for what shipped since v0.6.0

Every claim below is checkable against the repo. The constraints in
`copy-constraints.md` govern: in particular, the second audio track is a
**Twitch Enhanced Broadcasting** capability, it needs a **GPU**, and no other
platform has been measured as accepting one.

---

## 1. `/features` — a new section, placed SECOND

Placed directly after `#routing` and before `#matrix`. It is the strongest thing
added since the tag, and the page's own argument is that routing comes first and
everything else follows from it — this follows from it more directly than
anything else on the page.

```
id:      vod
eyebrow: Two mixes, one connection
title:   The broadcast keeps the music. The archive does not.
```

**Body, paragraph 1**

> Twitch Enhanced Broadcasting negotiates a second audio track alongside the
> live one. polyemesis builds both from the same ingest, in one filter graph,
> and sends them over one connection — the live mix with the music bed, and a
> VOD track without it. No second upload, no second encode of the picture.

**Body, paragraph 2**

> The negotiation is measured against Twitch's own endpoint rather than assumed.
> A single video rendition is enough — multitrack *video* is not a precondition
> for the second audio track, which is the part most people expect to be
> required. A secondary mix that will not compile is a warning and the broadcast
> continues; an optional archive track must never take a working stream down
> with it.

**Body, paragraph 3 — the limits, stated in place**

> It needs a GPU. Twitch refuses the negotiation outright if the encoder reports
> none, so this is the one feature here that a headless VPS cannot use. Every
> other destination still receives its own single mix, exactly as before.

**Body, paragraph 4 — the cost, because the site claims "0 re-encodes"**

> The picture is still copied, not re-encoded. The second audio track is a
> second AAC encode, which is cheap and is not free — two mixes cost two audio
> encodes, and the site's "0 re-encodes" has always meant the video.

Without this the page contradicts its own headline number for anyone who reads
both. A reader who notices it themselves concludes the stat is marketing.

**Caption for the screenshot** (reuse `02-routing.png` or shoot the destination
dialog showing the VOD toggle):

> The VOD track is per destination, and off unless asked for.

---

## 2. `/features` — the chat section says four, and it is five

**MY FIRST DRAFT OF THIS WAS WRONG, and the review caught it.** I had Rumble
joining the send fan-out. It does not. The adapter implements no `Send`,
`Delete`, `Ban` or `Timeout` — it is poll-only ingestion, by design, because the
Rumble live-stream API offers no socket, no webhook and no long poll.

There is a second caveat the adapter states about itself and the site must not
paper over: nothing in it has been run against a real key on a live broadcast,
because obtaining one requires a Rumble account login. The transport and the
refusal path are verified; field-by-field fidelity of a live payload is
documented rather than observed.

So the timeline is five. The send fan-out is still three.

**Current:** "Four platforms in one timeline"

**Replace with:** "Five platforms in one timeline"

**Current body, first sentence:**

> One merged feed with one send box that fans out to YouTube, Twitch and Kick —
> Facebook is read and moderate only — with per-platform verdicts when half the
> fan-out fails, because partial delivery is the normal case.

**Replace with:**

> One merged feed reading YouTube, Twitch, Kick, Facebook and Rumble. The send
> box fans out to YouTube, Twitch and Kick, with per-platform verdicts when half
> the fan-out fails, because partial delivery is the normal case. Facebook is
> read and moderate only; Rumble is read only, and polls, because its API offers
> no socket.

Both caveats have to survive the edit. Naming which platforms are read-only in
the same sentence as the count is the difference between "five platforms" being
informative and being a lie of omission.

---

## 3. `/comparison` — one new row

| Capability | polyemesis | Restreamer | restream.io |
|---|---|---|---|
| A second audio mix to the same destination, from one ingest | Yes | — | — |

The competitor cells are left blank rather than "No" until they carry a dated
source, per §1.3 of `SITE-IMPROVEMENTS.md`. A row that asserts a No we have not
checked is exactly the unsourced-claim problem that review already flagged.

Add to `docs/COMPARISON.md` in the same edit, or `check-build.mjs` fails the
build — the capability-row parity check requires every site row to appear there.

---

## 4. Landing page — one new item in "What else it does"

The grid is six items. This makes seven, or replaces the weakest.

**Title:** A separate archive mix
**Body:**

> Twitch Enhanced Broadcasting takes a second audio track. The live mix keeps
> the music; the VOD track does not. One ingest, one video encode. Needs a GPU.

---

## 5. The use case, in the words someone would search

The site never says "DMCA", "copyright", "muted VOD" or "music" as a problem,
and those are the words a streamer types. This belongs on the landing page in
the problem section, as one added sentence:

> It is also the shape of the music problem: the stream that goes out live can
> carry the bed, and the track kept for the archive can leave it out — as long
> as the platform will take a second audio track, which today means Twitch.

Deliberately not: "avoid a DMCA strike", "keep your VOD safe", or anything that
reads as a promise about what a rights-holder or a platform will do. Describe
the mechanism, let the reader draw the conclusion.

---

## 6. `/features` "Is this for you?" — one line in the not-for column

The landing page's not-for list should gain:

> …if you need the Twitch archive track and your server has no GPU — the
> negotiation requires one.

---

## What NOT to add

- **No claim that other platforms take a second track.** polyemesis can emit
  one; only Twitch has been measured as accepting it, and the `-c:a copy`
  refusal for RTMP destinations is still live and still correct.
- **No "automatically music-free VOD".** We build and send the mix; what the
  platform and a rights-holder do with it is not ours to promise.
- **No mention of multitrack video.** polyemesis negotiates one video rendition.
  Enhanced Broadcasting is named for multitrack video and implying we do it
  would be the easiest false claim on this page to make.
