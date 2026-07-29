# Design system

One palette, one type scale, one motion vocabulary — shared by the **app** and
the **website**, so the marketing page and the console are recognisably the same
product.

Sourced from a review of comparable products on Mobbin, 2026-07-29, with the
polyemesis column verified against `ui/src/index.css` rather than assumed.

- [What the review found](#what-the-review-found)
- [Tokens](#tokens)
- [Sharing tokens with the website](#sharing-tokens-with-the-website)
- [Component rules](#component-rules)
- [The website](#the-website)
- [What is deliberately absent](#what-is-deliberately-absent)

---

## What the review found

The starting question was whether polyemesis's theme needed replacing. **It does
not.** The existing thesis — *a broadcast console, not a SaaS dashboard*;
near-black surfaces; saturation reserved for signal — is the same thesis the
strongest products in this category arrived at independently.

| Product | What it confirms |
|---|---|
| [Substack Live](https://mobbin.com/screens/cead5524-3612-4935-ac7c-9c9e5529cb28) | A live view needs almost no chrome: `LIVE`, elapsed time, viewer count. Nothing else competes |
| [Riverside](https://mobbin.com/screens/5f2ce98e-e3ac-4746-a64f-b1bc76d4f1cc) | Every control carries a **text label under its icon**. Icon-only is for apps where a mis-click is cheap |
| [Vimeo](https://mobbin.com/screens/6caeef01-524d-4452-b645-36f30e9a6967) | Dense settings belong in a **collapsible accordion** by concern, not one long column |
| [Modal](https://mobbin.com/screens/fa75138f-7ba1-4f9e-a5a0-d1c150ffe76e) | Exactly one saturated accent, on the announcement bar, half the headline, and the primary CTA |
| [Neon](https://mobbin.com/screens/93596986-f1bd-45fb-bde2-06af7bc96a2e) | A hero that is a **spectrum bar field** — a picture of signal |
| [Linear](https://mobbin.com/screens/887269c3-bc0d-4090-aaef-13c4462a1b53) | No hero illustration at all: large type, one subhead, then a real product screenshot |

Two findings changed the plan:

**Every comparable developer-tool site is dark.** Modal, GitHub, Neon and Linear
are all near-black. The assumption that a marketing site must be light was
wrong, and dropping it removes the largest piece of work — a second full palette
— and means the app's tokens ship to the website unchanged.

**Neon's hero is decorative signal; ours can be real.** polyemesis already
renders audio meters from `--meter-low` through `--meter-peak`. The hero can be
that component with a recorded fixture behind it. A product whose entire
argument is *per-destination audio from one ingest* should lead with a meter,
not with a gradient blob.

## Tokens

`ui/src/index.css` is the only place a colour is defined, and no component may
hardcode one. That rule already held for colour. It now holds for type, motion
and elevation too, which previously had **no tokens at all** — every size and
duration was written inline at the point of use, which is how a scale drifts.

### Colour — unchanged

81 tokens, already correct. The rule that matters:

> Saturated colour is **signal only**. Green is live, amber is reconnecting, red
> is down or clipping, cyan is armed but idle. If everything is grey except the
> thing that is wrong, the thing that is wrong is readable from across the room.

Nothing decorative may use `--live`, `--warn`, `--down` or `--armed`. The
interactive accent is one desaturated slate blue, and it is the only colour a
button may be.

### Type scale

A console shows numbers that are read at a glance and prose that is read
carefully, so the scale is tighter at the small end than a marketing scale would
be — six steps, no more, because a seventh gets chosen by accident.

| Token | Size / line | For |
|---|---|---|
| `--text-micro` | 10px / 14px | Units, axis labels, meter scales |
| `--text-tiny` | 11px / 16px | Hints under a field, secondary stats |
| `--text-sm` | 12px / 18px | Table cells, form labels — the console default |
| `--text-base` | 14px / 20px | Body prose, dialog copy |
| `--text-lg` | 18px / 24px | Card titles |
| `--text-display` | 28px / 32px | Page titles, and the website's step below the hero |

`--font-mono` carries anything an operator might copy or compare
character-by-character: stream keys, URLs, topic names, hex colours, timecodes.
That is a correctness rule, not a stylistic one — a proportional font makes `l`
and `1` in a stream key indistinguishable.

### Motion

Three durations. A console animates to explain a state change, never to
decorate one.

| Token | Value | For |
|---|---|---|
| `--motion-instant` | 90ms | Hover, focus ring, button press |
| `--motion-quick` | 160ms | Disclosure, popover, tab change |
| `--motion-settle` | 260ms | Dialog, drawer, page transition |

Nothing that reflects **live state** may animate its arrival. A meter, a status
pill and a viewer count change because reality changed; easing them in adds
latency to the one number the operator is watching. `prefers-reduced-motion`
collapses all three to zero.

### Elevation

Four levels, expressed as surface colour first and shadow second, because a dark
UI reads depth from lightness more than from shadow.

| Token | Surface | Shadow |
|---|---|---|
| `--elev-flat` | `--surface` | none |
| `--elev-card` | `--card` | none — the border carries it |
| `--elev-raised` | `--card-raised` | `--shadow-raised` |
| `--elev-overlay` | `--popover` | `--shadow-overlay` |

## Sharing tokens with the website

The tokens live in `ui/src/index.css`, which the website cannot import. Copying
them is how two products stop looking like one.

The system's source of truth is therefore a **plain CSS custom-property block**
with no Tailwind syntax in it — the `:root` section of `index.css`. It is valid
CSS anywhere, so the website includes the same block and gets the same palette,
type scale and motion without depending on Tailwind, a build step or a package
registry.

The `@theme inline` block below it is the Tailwind adapter and is app-only. That
split is the whole mechanism: **`:root` is the system, `@theme` is one
consumer.** A website built in anything at all consumes the first half.

## Component rules

Derived from the review, and each one is a decision rather than a preference:

1. **Icons carry labels.** Every control in a bar or rail has a text label, as
   Riverside's does. Icon-only tooltips are for applications where a wrong click
   is recoverable; here it ends a broadcast.
2. **Dense settings collapse by concern.** A form past roughly eight controls
   becomes an accordion grouped the way Vimeo's is. The go-live composer already
   crossed that line — it carries title, description, category, tags, scheduled
   start and three toggles.
3. **A disabled control says why, next to itself.** Never a bare disabled state.
   The broadcast-settings toggles name the lifecycle state that locked them.
4. **A warning that is normal is not red.** `mqtt://` on a trusted LAN and a
   plaintext broker password are amber and explained, because red trains people
   to dismiss red.
5. **Numbers are monospaced and right-aligned** wherever two of them will be
   compared.
6. **Live state never animates in.** See Motion.

## The website

Structure, taken from the four sites above, which agree almost exactly:

- Thin announcement bar, accent background, one sentence, one link
- Near-black hero, headline at two lines maximum
- One-line subhead that says what it *is*, not what it *enables*
- Filled primary CTA plus an outlined secondary — never two filled
- Below the fold: **the product**, either a live meter or a real screenshot

The hero should be the meter. It is the one thing no competitor's landing page
can show honestly, because none of them route audio per destination.

## What is deliberately absent

- **A light theme.** Every comparable product is dark, the app is used in dark
  rooms, and a second palette doubles the surface where a contrast bug can hide.
  Revisit only if a real operator asks.
- **A component library package.** Two consumers do not justify a published
  package and its release process. The CSS block is the contract.
- **Iconography rules.** lucide-react is already consistent; a rule here would
  describe what already happens.
- **Illustration.** There is no budget for it and no need: the product renders
  its own best visual.

## See also

- `ui/src/index.css` — the tokens themselves, and the only place they live
- [ARCHITECTURE](ARCHITECTURE.md)
- [MONITORING](MONITORING.md) — what the signal colours mean operationally
