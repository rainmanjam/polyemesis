# Item zero: features that are built and cannot be reached

**Status:** confirmed by measurement, 2026-07-28.
**Estimated cost:** 1–2 days.
**Recommendation:** do this before starting any new feature on the roadmap.

---

## The finding

Three fields exist on the Go `Rendition` model, are persisted, are validated, are
compiled into FFmpeg arguments, and are covered by tests — and appear **nowhere
in `ui/src`**:

| Field | What it controls | Where it works today |
|---|---|---|
| `aspectMode` | `crop` / `pad` / `blurpad` — the whole of dual-format output | [internal/ffmpeg/rendition.go:326](../../internal/ffmpeg/rendition.go) |
| `padColor` | the letterbox fill for `pad` | same |
| `deinterlace` | `bwdif` with `off` / `auto` / `all` | [internal/ffmpeg/rendition.go:302](../../internal/ffmpeg/rendition.go) |

```console
$ grep -rn "aspectMode\|deinterlace\|padColor" ui/src/
$ echo $?
1
```

Nothing. The TypeScript `Rendition` interface in `ui/src/lib/types.ts` stops at
`gopSeconds` and `note`.

So an operator cannot produce a vertical rendition, cannot letterbox, cannot use
the blurred-pad treatment, and cannot deinterlace — not because any of it is
unimplemented, but because there is no control.

## Why this is worth a heading of its own

These are not stubs. `AspectBlurredPad` in particular is carefully built and
carefully justified in the source:

> This is the convention every vertical feed has settled on, and it is the
> difference between a repurposed landscape stream looking deliberate and
> looking lazy.

The blur is computed on a 1/8-scale proxy because a full-resolution gaussian
"costs more per frame at 1080p than the H.264 encode it feeds". Someone thought
hard about this. It is finished work, and it is invisible.

Likewise deinterlacing uses `bwdif` rather than `yadif` for a stated reason, with
`mode=send_frame` chosen because `send_field` would double the frame rate and
"silently doubles the bitrate a platform receives and breaks the GOP arithmetic".
Also finished. Also invisible.

**This matters for planning, not just for tidiness.** The competitive research
listed deinterlacing as a GAP and ranked it fourth of six by evidence. Building
it would have meant reimplementing something already correct. The roadmap was
about to budget days for a select element.

## This is the third instance of the same failure

[DESIGN-ONE-PORT-ONLY.md](../DESIGN-ONE-PORT-ONLY.md) already records the
pattern, in a paragraph that reads as if written about this:

> **It shipped off.** The feature was built, tested and documented, and then no
> new install used it. The default is the product for almost everyone.
>
> **The shared port was unreachable anyway.** It defaulted to 6100, which
> `docker-compose.yml` never published and no document mentioned. […] A feature
> that is off by default and broken when turned on is not really a feature.

Same shape, third time. A feature is complete in every layer except the one a
person touches, and nothing in the build fails.

## Why the existing guard did not catch it

The project already identified UI/Go mirroring as a hazard and built a test for
it — [internal/db/limits_drift_test.go](../../internal/db/limits_drift_test.go):

> Mirrored constants drift, and this pair drifts silently in the worst
> direction: if the UI permits more than the server accepts, the operator gets a
> save button that does nothing […] So the mirror is checked rather than
> trusted.

That is exactly the right instinct. But it guards **numeric bounds**, and the
drift happened in **field presence**. The guard covers one axis of a two-axis
hazard.

A first attempt at measuring the wider drift reported 19 missing fields across 5
types. **That number was wrong**, and the way it was wrong is instructive — most
of it was legitimate structural difference rather than drift:

| Apparent drift | Verdict |
|---|---|
| `ChatMessage` — 8 fields | Not drift. The UI nests them in a `ChatAuthor` object |
| `Destination` — the three expert-mode fields | Not drift. The UI models them as `ExpertArgs` / `ExpertGuard` / `ExpertResponse`, deliberately a different shape |
| `Settings.failover` | Not drift. Fetched and typed separately |
| **`Rendition` — `aspectMode`, `deinterlace`, `padColor`** | **Real** |

A field-presence check therefore cannot be a naive set difference; it would cry
wolf on every intentional restructuring and be switched off within a month. What
it can do honestly is assert an **explicit allowlist of known-different shapes**,
so a *new* divergence fails the build while the four deliberate ones stay quiet.

## The work

1. Add `aspectMode`, `padColor`, `deinterlace` to the TypeScript `Rendition`
   interface.
2. Add the controls to `RenditionsPage.tsx`, beside the existing size and rate
   fields: aspect mode as a select with the four modes in the order
   `AspectModes` already declares (no-op first, then increasing work),
   pad colour behind `pad`, deinterlace as a three-way select.
3. Explain them where they are, not in a tooltip. `blurpad` needs one sentence
   about what it is for; `auto` vs `all` deinterlace needs the sentence about
   capture cards that flag everything progressive.
4. Extend the drift guard to field presence, with the allowlist described above.
5. An end-to-end check that a rendition created through the UI with
   `aspectMode: blurpad` actually produces the filter — the existing
   `rendition_test.go` bbox harness already proves the filter itself, so this
   only needs to prove the value survives the round trip.

Then update [COMPARISON.md](../COMPARISON.md) and
[RESEARCH-COMPETITIVE.md](../RESEARCH-COMPETITIVE.md), where deinterlacing is
now recorded as "built, unreachable" rather than missing.

## The question worth asking after this

Two features were found unreachable by looking at one model. Nothing systematic
has checked the others. Before the next feature ships, it is worth one pass
asking of each recently-added capability: *can a person actually turn this on?*

---

## See also

- [ROADMAP](README.md)
- [../DESIGN-ONE-PORT-ONLY.md](../DESIGN-ONE-PORT-ONLY.md) — the same failure,
  recorded the first time
- [../RENDITIONS.md](../RENDITIONS.md) — which documents aspect modes as
  available, and will need a note until this is fixed
