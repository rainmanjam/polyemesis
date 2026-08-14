# What the two-mix copy may and may not claim

Verified against the code and the commit record on 2026-08-14, before any copy
was written. These are the traps: each one is a sentence a reasonable person
would write that would be false.

---

## 1. "Every platform can now take two audio tracks" — FALSE

What was measured in #141 is that **polyemesis can emit** two distinct audio
tracks over RTMP, and that **polyemesis's own RTMP server receives them as two
distinct tracks** — 300 Hz on one, 5000 Hz on the other, 81 dB of band-balance
apart.

The commit says so itself:

> The #141 refusal that IS stated — `-c:a copy` on an RTMP destination, in
> `db.AudioEncoding.copyProblems` — is untouched. Its reason is about platforms
> accepting multitrack audio, and **nothing here measured a platform**.

And that refusal is still live in `internal/db/destinations.go:436`:

> platform ingests expect one encoded stereo track, so a copied multitrack
> stream would upload cleanly and be rejected

**Copy rule:** the second track is a *Twitch Enhanced Broadcasting* capability.
Do not imply YouTube, Kick, Facebook or Rumble accept one. We have not tested
them and the code's stated position is that they do not.

## 2. "Twitch gives you a separate VOD audio track" — TRUE, and measured live

Measured against the live `GetClientConfiguration` endpoint with no credential:

- `audio_configurations.vod` is populated, and depends on nothing but
  `preferences.vod_track_audio` — not on the account, not on a token.
- A multi-rendition video ladder is **not** a precondition:
  `maximum_video_tracks: 1` returns one rendition **and both audio tracks**.
  This is what makes it reachable for polyemesis at all, and it is worth saying
  plainly — a reader will assume multitrack video is required, because the
  feature is named for it.

## 3. "It works on any server you own" — FALSE for this feature

Twitch refuses a host that sends no GPU information:

> Your broadcast software (polyemesis) did not send GPU Information which is
> required by GetClientConfiguration

**Copy rule:** Enhanced Broadcasting requires a GPU. Say it in the same block
that announces the feature, not in a footnote. This audience self-hosts, often
on a VPS with no GPU, and discovering it after installing is the experience the
"Is this for you?" section exists to prevent.

## 4. "Your VOD is automatically music-free" — OVERSTATED

polyemesis builds and sends a second mix. What Twitch does with it — and whether
a given music rights-holder agrees — is not ours to promise. Describe the
mechanism, let the reader draw the conclusion:

> The live broadcast carries the music. The VOD track does not. One ingest, one
> encode, one connection.

Do not use the word "safe", do not promise a strike will not happen, and do not
give legal advice.

## 5. "Four platforms in one timeline" — NOW WRONG

`/features` says four. Rumble shipped (#312). It is five: YouTube, Twitch, Kick,
Facebook, Rumble.

Note the existing caveat still holds and must survive the edit: **Facebook is
read-and-moderate only** — it is not in the send fan-out.

## 6. Safety properties worth stating, because they are unusual

- A secondary mix that will not compile is a **warning**, not an error. An
  optional VOD track must never veto a working broadcast. A primary that will
  not compile is still an error.
- The empty namespace is byte-for-byte the previous output, so a destination
  that gains a VOD track does not have its live mix rewritten.

Both are the kind of thing this audience checks for and rarely gets told.

---

## The comparison row

Proposed: **"A second audio mix to the same destination, from one ingest"**

- polyemesis: **Yes** (Twitch Enhanced Broadcasting)
- Restreamer: **No**
- restream.io: **No**

Before this ships, the two competitor cells need the same dated-source treatment
the rest of the table is getting under §1.3 of SITE-IMPROVEMENTS.md. If we
cannot source them, the row states only what polyemesis does and leaves the
others blank rather than asserting a No we have not checked.
