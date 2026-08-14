# polyemesis — changes since v0.6.0 (2026-08-09)

132 commits. 21 features and security fixes, 30 bug fixes, 36 test commits.

This is the input to a website-copy review: what is now TRUE that the site does
not yet say.

---

## 1. A destination can receive TWO audio mixes over one connection

The headline change, and the one the site says nothing about.

**What shipped (#141, #326, #331):**

- `ffmpeg.DestSpec.SecondAudioOutLabel` names a second finished mix from a
  destination's filter graph and maps it as a second encoded audio track over
  RTMP. The old cap was never a stated refusal — it was one `-map [aout]` and a
  doc comment asserting "the single stereo track the platform will accept" as
  though the muxer were the constraint. Nobody had measured it. It is not.
- `routing.Compile` previously emitted one mix with fixed internal labels
  (`a_t0`, `a_mix`, `aout`), so concatenating two compiled graphs collided on
  every one of them. Every label site now goes through a namespace, and
  `CompilePair` returns both mixes in one `filter_complex` plus the label to map
  for the second.
- Twitch **Enhanced Broadcasting** (= Amazon IVS Multitrack Video) negotiates a
  VOD audio track, and refuses a GPU-less host rather than pretending.

**How it was proved:** `TestTwoDistinctMixesReachAnRTMPFarEnd` publishes the
exact argv `DestinationArgs` builds, through a real FFmpeg, into polyemesis's
**own** RTMP server — not a permissive listener — and reads the tones off each
received track: 300 Hz on one, 5000 Hz on the other, **81 dB of band-balance
apart**. Tones rather than a track count, because two tracks carrying the same
audio is a failure a count cannot see. A second subtest publishes one mix twice
and asserts the check catches it.

**Why it matters to a user:** the live broadcast can keep the music while the
VOD track does not — from one ingest, one encode, one connection. That is the
DMCA problem, solved without a second upload.

**Safety properties worth stating:** the empty namespace is byte-for-byte what
the package emitted before, so a destination that gains a VOD track does not
have its live mix rewritten. A secondary mix that will not compile is a
**warning**, not an error — an optional VOD track must never veto a working
broadcast. A primary that will not compile is still an error.

---

## 2. Chat reaches a fifth platform, and the platform list came from OBS

- **Rumble** (#312) — its live-stream API does carry chat.
- A **platform registry seeded from OBS** (#312): 529 of 540 ingest URLs carry
  an app path. `AnalyseURL` warns on a pathless URL; `CheckEncoder` compares
  platform ceilings.
- The Kick preset **could not publish** — shipped with a host that was wrong for
  everyone, and a note that denied a key we actually fetch over `streamkey:read`.

Chat now spans **YouTube, Twitch, Kick, Facebook and Rumble**. The site says
four.

---

## 3. Four separate stream-key disclosures, found and closed

All four are the same class — a credential reaching somewhere it was never
meant to — and all four are now guarded.

| # | What leaked | Where |
|---|---|---|
| #310 | a refused destination wrote its key to `server.log` **on every retry** | supervisor |
| #306, #307 | the stored spelling of a key and the spelling that reached the wire were allowed to differ | db → wire |
| #324 | the automod endpoint | api |
| #333 | a minted Twitch key, only *partially* masked — `SecretSet.Scrub` is `strings.ReplaceAll`, so registering a substring leaves the containing string half-masked, which reads as protection | twitch |

Plus: **destination stream keys are now sealed at rest**, and a key that will not
open disables its destination rather than failing open.

---

## 4. Uploads and media

- Probe an upload **before** accepting it, and show what it is.
- A refusal is a state the record can hold, not an error thrown away.
- A re-verify job that records **only what it established** (#202).
- Count the length of a raw elementary stream instead of refusing it (#218).
- The upload probe's queue, its rejection message, and two egresses the ledger
  never listed.

## 5. Engine and operations

- An inherited pull source is re-checked at reconcile (#255): unchecked warns,
  refused **stops the ingest**.
- Staging or rolling back the binary raises an audit event.
- A source reports what a delete would actually take with it (#305).
- `read` means metadata, and search was serving the words.

## 6. The website itself

Version in the footer from `git describe`; a lightbox for the screenshots;
screenshots re-shot against the current UI and 61% smaller at unchanged
resolution; the mobile horizontal overflow, the nav that marked no page as
current, the sticky comparison column, the touch affordances, the tablet band,
and the six-section conveyor belt on `/features`.

---

## What the site currently claims that is now WRONG or INCOMPLETE

1. **"Four platforms in one timeline"** on `/features` — it is five. Rumble
   shipped.
2. **Nothing anywhere mentions two mixes to one destination.** This is the
   largest capability added since the tag and the site is silent on it.
3. The comparison table has no row for a per-destination **VOD** track, which no
   competitor does either.
4. `/features` has no section for Enhanced Broadcasting / multitrack RTMP.
5. Nothing states the DMCA/split-audio use case in the words a streamer would
   search for, even though the product now solves it end to end.
