# Code review and security audit — PR #365

Five reviewers: two opus (refactor; tests/docs/labels), one fable driving codex
and agy for code, one opus and one fable for security (still running when this
was written). Every finding below was checked against the source; the ones I
verified personally are marked.

**Verdict: do not merge as-is.** Two blockers, both verified by running
something rather than by reading.

---

## Critical — block merge

| # | Finding | Evidence |
|---|---|---|
| **C1** | **The EXPERIMENTAL label asserts a falsehood that the test suite disproves on every run.** `internal/multitrack/multitrack.go:5` says *"THIS CODE has never talked to Twitch"*. `live_test.go` reaches `ingest.twitch.tv` on every run and passes. I ran it: Twitch's real refusal text came back, a live negotiation granted a VOD audio track, and Twitch minted a **314-character key** from the 44 it was sent. The same false claim appears in **ten places** — `multitrack.go:5,14,16`, `CHANGELOG.md:319,407`, `docs/AUDIO-ROUTING.md:43`, `DestinationDialog.tsx:1911`, `Experimental.tsx:9`, `SettingsPage.tsx:1378`, `RoutingPage.tsx:1004`. Beyond being wrong, it tells a maintainer there is no live test — the only canary for Twitch tightening its allowlist. | verified by running |
| **C2** | **The hardware label is false for VideoToolbox.** `TestEveryConfiguredEncoderOpensWithItsOwnFlags` runs a real 5-frame encode per registered encoder with that encoder's own flags, including the capped-VBR path. On this Mac `h264_videotoolbox` and `hevc_videotoolbox` both **pass**. Yet `rendition.go:241`, `ENCODING.md:153`, `RENDITIONS.md:259`, `HARDWARE.md:451` and `RenditionsPage.tsx:1697` all say hardware flags are unconfirmed. The UI badge is gated on `encoder?.hardware`, so a Mac operator is warned off the one hardware encoder that *is* verified — and having seen it fire falsely once, discounts it on `h264_nvenc` where it is true. | verified by running |

**The correct boundary** — narrower than the PR claims in both directions:

> The negotiation runs against `ingest.twitch.tv` and succeeds. What has never
> been observed is a broadcast **published through a minted key** — everything
> after `Negotiate` returns. For encoders: VideoToolbox is confirmed on real
> hardware; NVENC, QSV, VA-API and AMF are not.

---

## Major — fix before merge

| # | Finding | Raised by |
|---|---|---|
| **M1** | **Unguarded `applyPresetTo` race defeats the PR's own null guarantee.** `RoutingPage.tsx:306-318` awaits, then calls `apply(res.profile)` unconditionally — no cancellation, no destination guard, no enabled-check. Click a VOD preset, switch the second mix **Off** before the response lands, and it turns back on with `setDirty(true)`. Save, and a `vodProfile` is persisted where `null` was intended. Same race across a destination switch. | codex — **agy declared this path "airtight"** |
| **M2** | **Live-mix regression vs `main`.** `applyPresetTo` no longer sets `compiled`/`compileError`. Apply a preset that fixes an invalid profile: the Result card keeps the old graph, old warnings, old red error, and **Save stays disabled** for 180 ms + RTT. The panel's central claim — "the graph shown is the graph that will run" — fails on the *primary* profile. | opus |
| **M3** | **The second-mix footer claims the track *is* produced**, on exactly the path where Twitch may refuse. `RoutingPage.tsx:544-551` renders whenever `!vodBlockedByToggle`, which includes a Twitch destination with EB on — where `negotiateDestination` may refuse and one track is sent. `SecondMixCard` hedges correctly two cards above; the footer contradicts its own hedge. | opus + codex + agy |
| **M4** | **`provisional` (unprobed ingest) is unaccounted for.** `engine/destinations.go:123` drops the second mix on *every* platform when the source is unprobed, and unlike the Twitch branch sets no `vodDropped` — so nothing reaches the card either. The page already has `ctx.probed` and does not use it. | opus |
| **M5** | **Switching the second mix off destroys its configuration** with no confirmation and no undo; toggling back on re-seeds from the live mix. Next to a `ConfirmDestructive` component the codebase uses for this exact class of action. | opus |
| **M6** | **Two profile fields are not pinned through the API.** Eight mutations run against the round-trip tests; two survived — forcing `Mode = ModeMatrix`, and zeroing every `Tracks[i].Enabled`/`Track`. The test asserts `len(Tracks)` and `Gain[0]` and nothing else, so a handler that stored a matrix mix as simple mode would pass. | opus |
| **M7** | **Label coverage misses the highest-traffic surfaces.** `web/src/pages/features.astro:29-31` describes EB as working in three unqualified sentences — the single most likely place a user meets it. Also `comparison.astro:39`, `COMPARISON.md:111,225`, `ENCODING.md:30,240`, `COPY-CONSTRAINTS.md`, and `TROUBLESHOOTING.md` has no entry for either feature. | opus |

**Owner decision, reviewers disagreed:** a failing VOD compile blocks Save on the
live mix (`RoutingPage.tsx:414`). `internal/routing/pair.go:73` states the
principle that "an optional VOD track must never veto a working broadcast" —
this vetoes a *save*, not a broadcast, which is defensible, but it strands valid
live-mix edits behind an optional feature. codex called it Major; agy called the
same code "working correctly."

---

## Minor

Save has a 180 ms debounce hole where an uncompilable profile can be PUT
(`:414`). No `key` on either `ProfileEditor`, so a destination switch shows the
previous destination's graph for ~200-300 ms (`:492`, `:524`). Duplicated
accessible names across both editors — DOM ids are namespaced, so this is a
screen-reader and future-test problem, not a wrong-checkbox one
(`TrackRows.tsx:139,207,249`). `MusicRightsCard` shows "music is being sent"
inside a mix the banner says is not being sent (`:1160`). `vodBlockedByToggle`
reads a possibly stale `multitrack` (`:409`). `<Experimental>` renders on a
Twitch destination even with no second mix configured (`:1002`). `ctx`
invalidates at meter rate, re-rendering both editors in full (`:363`).
`exhaustive-deps` is not enforced, and the compile effect's correctness depends
on it (`.oxlintrc.json`). `CHANGELOG.md:49` says "twelve encoder profiles" where
the PR's own exemptions make it ten.

---

## Positive

- **`idPrefix` is complete and required rather than defaulted.** A programmatic
  sweep for `id=`/`htmlFor`/`getElementById`/`aria-controls`/`aria-labelledby`/
  `name=`/`key=` across the editor path found no surviving constant id. A third
  instance cannot inherit the collision by omission.
- **The out-of-order compile guard is a real improvement over `main`**, which
  had no `cancelled` flag — a slow first response could overwrite a fast second.
  The cleanup ordering makes an in-flight response unable to resurrect a dead
  editor's error, and that ordering is guaranteed by effect-definition order
  rather than incidental.
- **The null-vs-absent pair is genuinely pinned**, verified by mutation: "null
  treated as absent" kills only T2, "absent treated as null" kills only T3.
  Neither test is redundant, neither is vacuous. Reading back through the HTTP
  GET rather than the store is what kills the "stores but never marshals"
  mutation.
- **The engine's gate was already covered** by four pre-existing tests, and the
  API test correctly restricts itself to what only the HTTP layer can get wrong.
- **"What was decided appears on this destination's card" is a promise actually
  kept** — `DestinationCard.tsx:279` renders `vodAudioDropped` fed from
  `engine/status.go:299`.
- `rendition.go:250` — *"Not a gate. Every encoder here stays selectable"* is
  the right thing to write beside a label, and it is true.

---

## The finding that outlives this PR

**Six of twelve UI findings would have been caught by a component test, and this
repo has no harness** — only `lib/` unit tests and theme guards. The interaction
the PR exists to create is the one thing nothing can see. `@testing-library/react`
is the highest-value addition available here.

The other two majors are honesty findings that only *reading* could catch: they
require holding the UI copy against `engine/destinations.go` three packages away.
No rendered assertion tells you a true-looking English sentence is false about a
Go branch somewhere else.

## A note on method

C1 and C2 were both found by **running the thing the label was about**. Every
reviewer that only read the diff — including me, for most of this session —
repeated the claim back as fact. The lesson is not "reviewers missed it"; it is
that a claim about what has never been tested can only be checked by testing.
