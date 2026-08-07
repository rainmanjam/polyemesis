# Redesigning how a destination's video treatment is configured

Three independent reviews — codex, agy and opus — plus a survey of datarhei
Restreamer, OBS, Livepeer Studio, Wowza, MediaLive, Ant Media, Oryx/SRS,
MediaMTX, Owncast and Jellyfin. Every claim below that touches this repo was
checked against the code.

## What is actually wrong

Not aesthetics. Three structural problems:

1. **The cheap path and the expensive path are peers in one `<Select>`.**
   `DestinationDialog.tsx:1103` lists renditions plus a synthetic
   "passthrough" entry. Copying video is nearly free; a rendition is the most
   expensive thing on the page. A dropdown says they are the same kind of
   choice.

2. **The shared/ref-counted model — the reason renditions exist — is a
   sentence under a select.** "one encode, shared by every destination on this
   rendition" is true, load-bearing, and in the place nobody reads.

3. **Encodes are listed by user-chosen name**, so the picker shows what someone
   called it rather than what it does.

A fourth, found by reading rather than using: the dialog **fetches the usage
data and throws it away**.

```ts
// DestinationDialog.tsx:599-601
.then((rows) => setRenditions(rows.map((r) => r.rendition)))
```

`RenditionView` carries `destinations` and `enabledDestinations`
(`ui/src/lib/types.ts:390-394`). Both are discarded one line after arriving.
Everything needed to say *"Feeds 3 destinations · 2 enabled · already
encoding"* is already on the wire.

## The decision that split the reviewers

agy proposed retiring the Renditions page and creating encodes inline in the
destination dialog. codex argued for keeping it. **codex is right**, and the
reason generalises:

> A rendition is a reusable, source-owned processing resource with multiple
> consumers — not an attribute of one destination.

An editor for a shared resource, rendered inside one consumer's form, teaches
that it belongs to that consumer. The second destination to select it then
looks like it costs another encode, when it costs nothing. That is the exact
inversion this redesign exists to fix.

The field supplies the cautionary tale. **Oryx (SRS Stack)** split this into
two unrelated sidebar features — `Scenarios > Forward` and
`Scenarios > Transcode` — with no field on a destination naming which encode it
sends. That is the failure of putting management *and* decision on a separate
page. The answer is neither merge nor split: **decision on the destination,
management on its own page, and a link between them.**

## The design

Replace the `<Select>` with two radio cards. Whole card is the hit target.

```
Video treatment ───────────────────────────────────────────────────

  ●  Copy the source video                                Recommended
     -c:v copy — the source video exactly as your encoder sent it.
     No encode, no process, no CPU.

  ○  Use a shared video encode
     Changes the picture once and shares it between destinations.

     ┌────────────────────────────────────────────────────────────┐
     │ 1920×1080 · 60 fps · H.264 · 6000 kbps                      │
     │ "1080p60 for Twitch and Kick"                               │
     │ Feeds 2 destinations · already encoding                     │
     │ This destination joins the running encode. No new encode    │
     │ starts.                                        [Change]     │
     └────────────────────────────────────────────────────────────┘

     [Create a shared encode…]        [Manage shared encodes ↗]
```

Spec first, name second. This fixes "named by the user, therefore
unrecognisable" without removing names.

The consequence line is computed, not static, and has four states:

| situation | line |
|---|---|
| joining a running encode | `This destination joins the running encode. No new encode starts.` |
| joining an idle encode | `Starts one shared encode when an enabled destination uses it.` |
| leaving, others remain | `Twitch and Kick stay on this encode. Nothing else changes.` |
| leaving, last one out | `Stops the "720p30 backup" encode — no other enabled destination is on it.` |

The last two are the genuinely novel part: **nothing surveyed tells an operator
what happens to the encode they are leaving.** MediaLive's documented
remediation is literally "Make a note of the video encode, in case you need to
refer to it again." It is the same arithmetic as the join case, run backwards,
and it is free to compute.

### Progressive disclosure

When Copy is selected, everything below collapses. This is not invention —
Restreamer already does it (`FilterSelect` renders only `if (coder !== 'copy')`),
and it is the asymmetry made structural: the free path has nothing to configure.

### Duplicate

Add a third action on each encode row, seeded from the tier it copies.
Operators want "the same tier but 4500 kbps for the constrained uplink", and
without it they either edit the shared tier — silently changing another
platform's picture — or retype eight fields. MediaLive offers exactly this
(`Share the existing settings` vs `Clone the existing settings`).

Do **not** copy MediaLive's modal. Selecting an existing tier should stay
frictionless; a dialog taxes the good case. And the risk the modal guards
against is already covered — `RenditionsPage.tsx:1753` warns *"Saving restarts
this encode, and with it the N enabled destinations above. Their audio routing
is untouched."*

The Duplicate consequence line must say a duplicate is **a second encode, not a
free variation**.

## Explicitly rejected

**datarhei's `allowCopy` capability filter.** Restreamer suppresses the
passthrough option entirely when the source codec is not in the destination's
accepted list, driven by per-service capability metadata. Elegant, and wrong
here: `docs/PLATFORMS.md` and `db.PresetDisclaimer` take a deliberate position
against asserting platform ceilings — *"Being confidently wrong about one of
these numbers breaks a live stream."* A hard filter is that assertion, and it
would hide the cheap option on a guess.

If the effect is wanted, take **OBS's** version: keep incompatible entries
visible and annotate them (`CodecCompat.Incompatible="(Incompatible with %1)"`).
Advisory and reversible.

**Cost hints derived from platform limits.** If a hint is wanted, derive it from
the source-to-tier delta `sourceNotes()` (`RenditionsPage.tsx:326`) already
computes — "re-encoding 3840×2160 → 1920×1080" — never from a limits table.

## Naming

Leave "rendition" alone in the model and the docs. Unify only the four
phrasings of the *free* state, which currently reads as four different things:

- `"passthrough · copy"` (RenditionsPage)
- `"passthrough"` (DestinationDialog's sentinel)
- `play.passthrough: "Ingest (passthrough)"` (i18n)
- the untranslated `""` in the dialog's own select

Wowza is the warning here: it ships `Encode` in XML, "Preset" in the Manager and
"Stream Name Group" in the docs, for one concept.

## Ranked

| # | Change | Cost |
|---|---|---|
| 1 | Stop discarding `RenditionView` usage data | one line |
| 2 | Two radio cards replacing the `<Select>` | small |
| 3 | Spec-first rows in the picker | small |
| 4 | Computed consequence line, all four states | medium |
| 5 | Collapse everything under Copy | small |
| 6 | Unify the four phrasings of the free state | small |
| 7 | Duplicate action | medium |

## Leave alone

- The rendition model and its ref-counting. It is correct and it is the reason
  this is worth doing well.
- The Renditions page as a page.
- `RenditionsPage.tsx:1753`'s restart warning, and the delete-fallback warnings.
  They already do the hard job of naming a consequence before it happens.
- The word "rendition" itself.
- Per-destination audio staying on the Routing page. It is a different axis with
  a different lifetime; merging it would produce one enormous form.

## Where polyemesis already beats the field

Worth knowing, because two of these are worth *saying* in the UI:

- **Livepeer Studio** charges 11× more for transcoding than delivery and puts
  **zero** cost indication on the field that triggers it.
- **Ant Media** requires "Add new streams or restart the running streams" for a
  rendition to exist — a destination can never cause one. polyemesis's
  first-enabled-destination-starts-it is genuinely better.
- Restreamer's v2 string is the closest anything gets to a cost *model*:
  *"Each encoding requires additional CPU/GPU resources."* polyemesis can beat
  it by naming the actual number, which it already computes.
