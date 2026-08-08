# Finding 2 — the cold-start hold. Solution survey.

Researched 2026-08-07 by codex and agy independently.

## The tension, stated once

Destinations compile an audio routing graph from the ingest track layout. Before
any stream arrives that layout is a PLACEHOLDER — six stereo tracks that exist so
the UI has something to draw. Compiling against it is dangerous in a specific,
quiet way: a real 5.1 track becomes `pan=stereo|c0=c0|c1=c1`, which is valid
FFmpeg, so the destination starts, stays up, and publishes front L/R while
discarding centre, where dialogue lives. No error anywhere.

The guard fixes that by refusing to plan until a layout is measured. The
regression is that "measured" is a property of the INGEST, and the things that
stand in for a missing encoder — slate, playlist file, backup — have their own
layouts the ingest probe never sees. So a cold start with the encoder off air
holds every destination down, including the ones that would publish the slate.

**Both reviewers independently reached the same diagnosis: the planner is asking
the wrong question.** It asks "has the ingest been probed", when what it needs is
"is there a routable layout for whatever is currently on air".

## Candidate solutions

| # | Approach | Cost | Precedent claimed | Wrong when |
|---|---|---|---|---|
| **A** | **Active-source layout resolution.** Every selector candidate (primary, backup, slate, playlist) carries its own layout. The planner consumes the layout of whatever is *currently on air*, not the ingest's probe state. Slate declares its synthesised stereo bed at creation. | Medium. Needs a layout model per source kind, and a replan at each source switch. | Wowza `ModuleLoopUntilLive`, Flussonic source failover (both **unverified** — see caveat) | Slate/backup layouts are arbitrary and cannot be declared or probed ahead of time |
| **B** | **Layout provenance taxonomy.** Give every layout a state: `measured`, `generated`, `verified-file`, `declared`, `unknown`. Only non-`unknown` is routable. The six-stereo placeholder becomes an explicitly **non-routable display layout**. | Medium-high. Touches every producer and consumer of a layout. | — | You want a small fix; this is an architecture change |
| **C** | **Defensive matrix downmix.** Keep planning against a fallback layout, but build the graph with a BS.775 fold (`L = L + 0.707·C + 0.707·Ls`) instead of `c0=c0\|c1=c1`, so a wrong guess *degrades* rather than silently dropping dialogue. | Low-medium. Needs a matrix builder in Go rather than 1:1 index mapping. | — | You need discrete channel isolation preserved exactly |
| **D** | **Probe timeout with slate override.** Keep the guard, but release the hold after N seconds or on an explicit operator "off-air" flag. | Low. A timer on the guard. | — | ffprobe fails permanently — destinations stay held for the whole timeout, every time |
| **E** | **Operator-declared layout.** Let the operator declare "8 stereo" up front; ffprobe later verifies and alarms on mismatch. | Low code, **high operational risk** — drift silently loses dialogue until validation | SRS has coarse audio-present/video-present defaults | Unattended deployments, or sources whose topology changes |
| **F** | **Reuse the last measured layout.** Persist last-known-good, use it on cold start. | Trivial — and **unsafe**. An encoder change recreates exactly the valid-but-wrong graph the guard exists to prevent, and a probe failure leaves the stale assumption forever | codex: found no project precedent for treating stale audio topology as routing truth | codex recommends outright rejection. So do I |
| **G** | **Status quo — strict hold.** | Minimal code, near-zero wrong-audio risk, but slate/playlist/backup are ineffective in exactly the cases they exist for, and it fails permanently when probing is unavailable | none found | Any system claiming off-air continuity — which this one does |

## Where the two reviewers agree

Both recommend **A**, and both frame the invariant the same way. codex states it
crisply:

> A layout may be *shown* in the UI without being *routable*. An FFmpeg routing
> graph may be compiled only from a source descriptor that is either measured
> from that source or explicitly verified for that source.

Both also accept the same cost: **seamless destination continuity across a
layout-changing source handoff is given up.** A short controlled reconfiguration,
or a continued slate, is observable and recoverable. Silently publishing front
L/R while losing dialogue is neither.

## Where they differ, and it matters

agy adds **C** on top of A: even with correct layout resolution, build fallback
graphs with a proper downmix matrix so a wrong guess folds centre into L/R rather
than discarding it. That is worth taking seriously independently of A, because it
attacks the ORIGINAL hazard rather than the guard around it — it converts a
silent failure into a lossy but audible one. The guard exists because
`pan=stereo|c0=c0|c1=c1` is silent about what it throws away; a BS.775 fold is
not.

codex is more cautious and explicitly warns against a shortcut I was considering:

> Treating `selSig != ""` as "known" is not safe.

Seamless switching among arbitrary primary/backup/playlist layouts is impossible
with the current copy-only selector. Any fix that assumes "the selector is
running, therefore the layout is fine" is wrong.

## Caveat on the citations

I asked both reviewers for real, verifiable GitHub issue URLs and told them to
say plainly if they found none. **Neither returned issue numbers I can verify.**
The named precedents — Wowza `ModuleLoopUntilLive`, Flussonic source failover,
SRS metadata defaults — are plausible and worth checking, but treat them as
leads, not evidence. codex was appropriately explicit that it found no project
precedent for the one bad option (**F**), which is the sort of negative result
that suggests it was actually looking.

The absence of a clear precedent is itself informative: per-destination audio
routing from a multitrack ingest is the thing polyemesis exists to do that the
comparable projects do not, so the layout-provenance problem may genuinely not
arise for them.

## Recommendation

**A, with C as a follow-up.** A fixes the regression and is the change both
reviewers independently arrived at. C is the deeper fix and can land separately,
because it makes every fallback path fail loudly instead of quietly.

Not **D** — a timeout that releases the hold reintroduces the original bug on a
schedule. Not **F** — actively unsafe. Not **G**, which is where we are now.
