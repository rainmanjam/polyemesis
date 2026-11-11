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

### Colour

42 tokens carrying a literal colour, each mapped to a Tailwind name in the
`@theme inline` block below. Count them with:

```sh
grep -cE '^\s*--[a-z-]+:\s*oklch\(' ui/src/index.css
```

That command used to match `#`, and therefore counted zero: the palette has been
written in `oklch()` since before this document existed. The number beside it
was right and the way to check it was not, which is the same failure as the rest
of this page in miniature. (It then said 37 while the file held 38, so run it
rather than trusting the figure.)

Surfaces, text and the primary blue are the values they have always been. The
signal tokens were reworked in the 2026-11 design review, because one green
meant running, healthy and on air at once, the failed red sat next to where an
on-air lamp has to go, and idle spent a cyan beside the interaction blue:

| Token | Value | Meaning |
|---|---|---|
| `--ok`, `--ok-dim` | `oklch(75.6% 0.184 149.9)`, `oklch(32.4% 0.0629 154.8)` | Healthy, signal present. Was `--live`, same values |
| `--warn`, `--warn-dim` | unchanged | Reconnecting, degraded, stale |
| `--down`, `--down-dim` | `oklch(70% 0.175 33)`, `oklch(28% 0.06 33)` | Failed, clipping. Coral, lighter than the old red, so it is never read as the tally |
| `--idle`, `--idle-dim` | `--muted-foreground`'s and `--muted`'s values | Configured but idle. Was `--armed` (cyan); neutral now, told apart by its hollow ring |
| `--tally`, `--tally-foreground`, `--tally-dim` | `oklch(56.5% 0.23 28.5)`, `oklch(99% 0.004 28)`, `oklch(25% 0.07 28.5)` | ON AIR and REC. New |
| `--cat-cyan`, `--cat-cyan-dim` | `oklch(72.1% 0.1065 216.8)`, `oklch(30.2% 0.0421 220.8)` | Identity, never status. New, carrying the old `--armed` cyan |
| `--destructive`, `--destructive-foreground` | `= --down`, dark ink `oklch(15.9% 0.0091 264.3)` | White on the coral is 2.87:1; the dark ink is 6.76:1 |
| `--meter-peak` | `= --down` | The meter's red zone and CLIP |
| `--meter-high` | removed | Amber left the meter |
| `--subtle-foreground` | `oklch(62.1% 0.0282 261.1)` | Placeholder, disabled. Was 51.2%, which was 3.41:1 on the shell; the website's value, now shared |
| `--ring` | `--primary-bright`'s value | The focus ring. 7.13:1 on `--card` where `--primary` gave 4.49 |

The rules that matter:

> Saturated colour is **signal only**. Green is ok, amber is warn, coral is
> down or clipping, and tally red is on air. Idle is not a colour. If
> everything is grey except the thing that is wrong, the thing that is wrong is
> readable from across the room.

- **Tally is a lamp, never ink.** `--tally` appears only as a solid fill under
  its own `--tally-foreground` label (4.97:1), for ON AIR and REC and nothing
  else. As text it would be the on-air signal shrunk to a word, and at 3.47:1 on
  `--card` it fails AA besides. The lamp's L 56.5% is the lightest a white label
  survives: at L 63.5% white measures 3.67. (The home page has one marketing
  exception, the bullet below.)
- **Marketing exception, the home page only.** This is a deliberate exception
  to the rule above. The owner decided it on 2026-11-10 ("Keep the red"). The
  app's rule is unchanged, and the logo is unchanged.
  - `.btn-onair` (a `--tally` fill under `--tally-foreground`, 4.97:1) is the
    homepage's primary CTA, Install included. The on-air lamp, the how-it-works
    rail and the step pips are tally fills too.
  - Tally is still never ink. The Nav, the other pages and the console keep the
    blue `--primary`.
  - The upload meter's over-limit bar uses `--down`, because "over your upload"
    is a failure state, not decoration.
- **Blue is interaction only:** links, focus and the primary button. Blue is
  never a state, a chart series or decoration. `--primary` is **fill-only**
  (buttons, checked controls, a lit crosspoint); every blue that is text is
  `--primary-bright`, because `--primary` as text measures 4.49:1 on `--card`.
  On the home page the primary CTA is the tally `.btn-onair` instead; see the
  Marketing exception above.
- **Amber means warn only.** It is not a meter stop and not a crosspoint. The
  meter is green, yellow and red; the website's lit crosspoint reads `--primary`.
- **Identity colours are never status.** `--cat-cyan` tells one *thing* from
  another: the Facebook chat accent, a hashed chat-author colour, the moderator
  shield, caption speaker labels, the website's mic track chip. It never goes on
  a status dot, pill or badge. It is held at 4.5:1 as text on every surface and
  at least ΔEOK 0.10 from `--ok`, `--warn`, `--down` and `--tally`. The
  Facebook filter chip differs on and off by text colour and border as well as
  fill.
- **Gradients appear only in the logo mark.** A colour fade anywhere else is
  decoration. The website's hero sweep (a blue light crossing the grid) was
  removed for this; the grid itself is 1px lines drawn with gradient syntax, a
  pattern rather than a fade.
- **Translucent text is measured, not assumed.** A `text-*/NN` opacity modifier
  is a new colour; `palette-contrast.test.ts` composites each one over every
  surface and fails it below 4.5:1. The same goes for a translucent hover fill,
  which is why the primary button hovers to solid `--primary-bright` rather than
  `bg-primary/85` (3.84:1).

Nothing decorative may use `--ok`, `--warn`, `--down`, `--tally`, `--idle` or
`--cat-cyan`.
The interactive accent is one desaturated slate blue, and it is the only colour
a button may be (apart from the start and stop actions, which carry their
signal).

`ui/src/lib/palette-contrast.test.ts` holds all of this to its numbers. It
computes WCAG ratios from the token values (oklch to linear sRGB, clamped as a
browser clamps) and fails below threshold: every text token on every surface at
4.5:1, every pill's ink on its fill, ink on the inert fills, every Button
variant at rest and on hover (read from `button.tsx`), the tally label on the
lamp, and the focus ring, tally lamp and meter segments at 3:1. It asserts the
decisions directly where a threshold would let an old value back in: the ring
is `--primary-bright`, idle is low-chroma, `--destructive` and `--meter-peak`
equal `--down`, `--down-dim` shares its hue, the site crosspoint reads
`--primary`. It fails if the tally drifts within ΔEOK 0.1 of the failed,
destructive or meter red, if any source paints text with `--primary`, uses
`--tally` as ink, or dims a text token below AA with an opacity modifier.
Reverting any one of the 2026-11 token changes fails it.

**Text fields have their own edge.** `--input` was `--border`'s value, 1.24:1
against the `--card` a field sits on (the field's `--background` fill is
1.09:1), so nothing reached the 3:1 WCAG 1.4.11 asks of a component boundary.
It is now its own lighter step, `oklch(55% 0.03 263)`: 3.66:1 on `--card`, 3.39
on `--card-raised`, 3.56 on `--popover`, 3.28 on `--muted` and 4.00 against
the field's own fill. A field is a control, not a divider; `--border` keeps
its value for hairlines. The contrast test holds `--input` at 3:1 on every
ground a field is drawn on.

**Charts and meters are flat.** The audio meter lights three discrete zones,
green below -18 dBFS, yellow from -18 to -2 and red from -2, each one flat
colour starting exactly at its break; the zone marks, the hatch above -6 and the
CLIP flag still carry the reading without hue. The Monitoring bitrate chart is a
linear `--ok` trace that breaks where samples are missing (a gap longer than
three samples), with no fill. Its one threshold line is the programme's
degraded-link floor, drawn in `--warn` only while that failover is on: a chart
does not invent thresholds nobody set. `lib/no-gradients.test.ts` fails on any
gradient in `ui/src` outside its allow-list (the overlay preview's transparency
checkerboard, a pattern rather than a fade).

### Type scale

**Case.** Form labels are sentence case at 12px/500. Capitals mark a kind of
thing rather than name a field, so they stay on table headers, menu group
labels, status pills and meter scales, all at one letter-spacing,
`--tracking-caps` (0.08em, the `tracking-caps` utility). Headings balance their
lines (`text-wrap: balance`) and paragraphs avoid a one-word last line
(`text-wrap: pretty`), both from the base layer.

A console shows numbers that are read at a glance and prose that is read
carefully, so the scale is tighter at the small end than a marketing scale would
be — six steps, no more, because a seventh gets chosen by accident.

| Token | Size / line | For |
|---|---|---|
| `--text-micro` | 10px / 14px | Units, axis labels, meter scales. **Never content.** |
| `--text-tiny` | 11px / 16px | Metadata only: a count, a timestamp. Content is 12px or more. |
| `--text-sm` | 12px / 18px | Table cells, form labels — the console default |
| `--text-base` | 14px / 20px | Body prose, dialog copy |
| `--text-lg` | 18px / 24px | Card titles |
| `--text-display` | 28px / 32px | Page titles, and the website's step below the hero |

`--font-mono` carries anything an operator might copy or compare
character-by-character: stream keys, URLs, topic names, hex colours, timecodes.
That is a correctness rule, not a stylistic one — a proportional font makes `l`
and `1` in a stream key indistinguishable.

#### The 12px floor for content

Anything a person reads as content is at least `--text-sm` (12px): labels,
values, warnings, notes, status words, badges and table headers. `text-micro`
(10px) is for units and axis or scale labels only. `lib/text-size-floor.test.ts`
enforces this as a ratchet: a file may hold no more `text-micro`, `text-[9px]`
or `text-[10px]` than its recorded count, and the shared primitives in
`components/ui` and `components/signature` hold the floor outright. Lower the
count when you convert a usage; do not raise it.

### Console kit

The shared patterns a page should use, rather than inventing its own. Each one
has a test, and each names the audit finding it closes.

**Empty state.** `components/EmptyState.tsx`. Props: `icon?`, `title`, `body?`
(one line), `action?` (one primary button). Use it wherever a list or panel has
nothing in it, so Routing, Clips, Recordings, Jobs and Chat answer the same way.
Every string is passed in already translated.

**Status words.** `lib/status.ts` returns a `StatusReading` (`labelKey`, `tone`,
`indicator`) for every domain: `processStatus`, `destinationStatus`,
`recorderStatus` and `clipBufferStatus`. Render it with
`components/StatusPill.tsx`. The tone is `ok` (on air, the only green), `warn`
(a transition, or something that needs a look), `bad` (failed) or `idle`
(not running by choice or by absence). The indicator is the non-colour cue:
filled, dashed, cross or ring. StatusPill draws an `ok` reading two ways. The
nominal case (a process running, a signal present) is neutral text with a 6px
`--ok` dot, because a column of identical filled green pills is wallpaper that
hides the row that is wrong. A reading marked `lamp` (an on-air destination, a
running recorder) is the solid `--tally` lamp under its `--tally-foreground`
label; the destination card's state badge reads the same flag, and the
header's live count and the Dashboard's ON AIR bar light the same lamp, so on
air is one red everywhere it is shown. Warn and bad keep their filled pills; idle keeps its outline. These are presentation words only. Never send
them to the server and never rename a state value; the API, MQTT, webhook and
Stream Deck vocabularies are contracts. A new state goes in `lib/status.ts`
with a test, not into a page.

**Two words for "not running"** (app-council A91). **Off** is something the
operator switched off: a disabled destination, a disabled feature. **Stopped**
is something that is on and not running: no process, or a process that
stopped. Do not add a third spelling ("Offline", "idle", "Disabled") to a
status pill, badge or count; the on-air counts use the same two words as the
cards. "Offline" stays only where it names something else: the live-updates
socket and a platform channel's own state.

**Confirmation.** `components/ConfirmDestructive.tsx`. Use it for every
destructive or live-affecting action; never `window.confirm`. Two conventions:

- **Name the effect.** Pass `consequence`, one short sentence that names the
  exact change on a live thing: "Stops publishing to YouTube. The broadcast can
  be restarted." A bare "Are you sure?" is not a confirmation.
- **Pick the tone.** `tone="destructive"` (the default) is for what does not
  come back: a deleted file, a revoked credential. `tone="caution"` is for a
  live action that can be undone, such as a stop, a cancel or making a stream
  public. Caution uses an amber icon and a default button, and its label is a
  verb. Pass `confirmLabel`; the fallback is "Confirm".

**Save model.** One rule for the whole console (A95). A **toggle or a choice
applies at once**, and the page says so; it never has a dirty state. A
**number, text or group of fields uses an explicit Save**: hold the edit in a
draft with `useSaveModel` (`lib/saveModel.ts`), show `SaveStatus` beside the
button, and keep the draft if the write fails. Never save on blur. A blur save
loses an edit that was never blurred and gives no sign that anything was written.

**Disabled with a reason.** `components/DisabledReason.tsx`. A title tooltip on
a disabled control never shows: the Button primitive sets `pointer-events-none`,
and a disabled button cannot take focus. So when a control is disabled, print
the reason as visible text beside it, and link the two with `aria-describedby`
(`disabledDescribedBy` in `lib/disabledReason.ts`). The reason says what would
unlock the control, not what is wrong with the operator. It renders nothing
while the control is enabled.

### Typefaces

Two shared families, and both halves of the product load the same two files.
The website adds Space Grotesk for display and IBM Plex Serif Italic for one
emphasis word on the homepage. Both are website-only.

| Token | Family | Served from |
|---|---|---|
| `--font-sans` | IBM Plex Sans | `ui/public/fonts/` and `web/public/fonts/` |
| `--font-mono` | JetBrains Mono | `ui/public/fonts/` and `web/public/fonts/` |
| `--font-display` | Space Grotesk | `web/public/fonts/` only |
| `--font-accent` | IBM Plex Serif Italic, stack `"IBM Plex Serif", serif` | `web/public/fonts/` only, not preloaded |

Self-hosted woff2, one variable file per family subset to latin, with
`font-display: swap`. Not a CDN link: a font request to a third party is a
request the operator did not make, and polyemesis is routinely run on an
isolated broadcast LAN where such a request does not fail fast — it hangs for
the connect timeout and then renders in Times.

The console also ships a second, smaller file per family,
`ibm-plex-sans-ext.woff2` and `jetbrains-mono-ext.woff2`, loaded under the same
family names with a `unicode-range`: Latin Extended-A/B and Additional,
combining marks, Cyrillic, arrows and a few currency signs. Without them a
Polish, Turkish, Russian or Ukrainian label rendered each accented or Cyrillic
letter in a system face. The browser fetches them only on a page that uses one
of those characters. They are app-only: the base files stay byte-identical
with the website's, which has no locale that needs them. Rebuild with
`scripts/fonts/build-ext-subset.py` (instructions at its top).

Japanese, Korean and Simplified Chinese have no self-hosted face: both
`--font-sans` and `--font-mono` name the operating system's own CJK faces after
Plex and JetBrains Mono under `:root:lang(…)`. A CJK subset worth shipping is
several megabytes per script, and every desktop OS an operator runs the
console on carries one.

The website also carries **Space Grotesk** for marketing headlines. The console
has no display type — its largest step is `--text-display`, a 28px page title —
so it does not ship that file.

The website also carries **IBM Plex Serif Italic** for the one emphasis word in
the homepage headline. It is the unmodified Latin-1 split file from the OFL
release (`@ibm/plex-serif` 2.0.0), about 24 KB (24,512 bytes), served from
`web/public/fonts/` only and not preloaded. Its fallback is the generic
`serif`.

This was the loudest half of the gap between this document and the code. The
website has loaded both families since it was written; the app declared
`--font-sans` and `--font-mono`, named no webfont at all, and fell through to
`ui-sans-serif`/`system-ui`. The two halves of one product rendered in different
typefaces, which explains more of why they did not look alike than any colour
does.

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

**Press.** A button moves down 1px while pressed (`active:translate-y-px`) over
`--motion-instant`, the website's `.btn:active` exactly. No scale: a control
that changes size shimmers in a dense row. A disabled button has
`pointer-events: none`, so it never presses.

**Focus.** One rule, in `index.css`'s base layer: `:focus-visible` draws a 2px
`--ring` outline at a 2px offset, which is also the website's ring. Components
do not set `focus-visible:outline-none` or `ring-*`; buttons and fields drew a
box-shadow ring with an offset painted in `--surface`, the wrong colour on a
card, while links drew this outline at 1px, and the controls
that had opted out of the outline without a replacement (tab triggers, the
dialog's close button) showed nothing at all. The one exception is a
programmatic jump target reached by a mouse click elsewhere (the destination
card the On Air bar jumps to), which `:focus-visible` ignores: it draws the
same outline on `:focus`.

### Elevation

Four levels, expressed as surface colour first and shadow second, because a dark
UI reads depth from lightness more than from shadow.

**There is no `--elev-*` token.** Elevation is a convention over tokens that
already exist rather than a layer of its own — writing `var(--elev-card)` gets
you nothing. Each level is the pairing below, applied directly:

| Level | Surface | Shadow |
|---|---|---|
| flat | `--surface` | none |
| card | `--card` | none — the border carries it |
| raised | `--card-raised` | `--shadow-raised` |
| overlay | `--popover` | `--shadow-overlay` |

All six of those are real tokens in `index.css`. If elevation ever earns its own
tokens, this table is the definition they should collapse to.

## Sharing tokens with the website

The system's source of truth is a **plain CSS custom-property block** with no
Tailwind syntax in it — the `:root` section of `ui/src/index.css`. It is valid
CSS anywhere, so the website includes the same block and gets the same palette,
type scale, motion and typefaces without depending on Tailwind, a build step or
a package registry.

The `@theme inline` block below it is the Tailwind adapter and is app-only. That
split is the whole mechanism: **`:root` is the system, `@theme` is one
consumer.** A website built in anything at all consumes the first half.

### How the block gets there

```sh
node scripts/sync-design-tokens.mjs
```

That copies the `:root` block verbatim into `web/src/styles/tokens.css`, which
`web/src/styles/global.css` imports. Run it after editing any token.

A checked-in copy rather than an import, because the website must not depend on
`ui/`: `web/Dockerfile` (built from the repository root with
`docker build -f web/Dockerfile .`) copies an explicit list of paths --
`web/`, `docs/` and the files the docs link to -- and `ui/` is not on it, so a
relative path reaching into `ui/` is a file the image does not contain. A shared
npm package is rejected under [what is deliberately
absent](#what-is-deliberately-absent), and generating the file during the Astro
build fails for the same reason the import does.

The copy is imported **after** `@import "tailwindcss"` and is unlayered, which
is what lets the shared `--font-sans` and `--font-mono` override the defaults
Tailwind puts in `@layer theme` without the website restating either family.

### What enforces it

`ui/src/lib/design-tokens.test.ts`, and this section is the reason it exists.
Between the day this document was written and the day anyone measured, the
sharing described above was never implemented and nothing said so: 13 of the
app's token names appeared on the website at all, `--surface`, `--muted` and
`--down` were different colours in the two products, and the two halves rendered
in different typefaces — while a comment in `index.css` described the palettes
as identical.

The test compares the two blocks token by token, fails when a value differs,
fails when the website restates an app literal instead of reading it, fails when
the app names a font family it does not self-host, and fails when the two
`public/fonts/` copies of a woff2 stop being byte-identical. It also fails when
its own walk finds nothing, because two stylesheets that yield no tokens agree
perfectly and mean nothing.

Where the two are deliberately allowed to differ, the reason is written beside
the value rather than left to a reader to infer:

| Site token | Why it is not shared |
|---|---|
| `--color-cohost`, `--color-game` | Track colours for legend chips. A track is a label, not a state, so none may read a signal token (`--color-meter`, the mic chip, now reads the shared identity token `--cat-cyan`; it used to borrow `--armed`, which went grey). `--color-cohost` sat 9.5° from the meter's yellow. Co-host is a magenta chosen for distance from every signal, meter and track colour (nearest: `--color-game`, ΔEOK 0.18) |
| `--color-primary-bright`, `--font-display`, `--motion-enter`, `--ease-breath` | Site-only; the console has nothing for them to do |

Two site tokens read an app token of a **different name**, because the names
collide and the meanings do not. `--color-surface` is a card fill, so it reads
`--card` rather than the app's `--surface`, which is a page. `--color-muted` is
secondary text, so it reads `--muted-foreground` rather than the app's `--muted`,
which is a slider track.

`--color-cross`, the lit crosspoint on the site's patchbay, reads `--primary`
(and `--color-cross-dim` reads `--primary-dim`). It used to be a literal kept
equal to the app's `--warn`; a crosspoint is the on-state of a control the
visitor toggles, so it takes the interaction colour, as the app's routing
checkboxes do, and amber means warn on both surfaces.

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
- Filled primary CTA plus an outlined secondary — never two filled. On the home
  page the filled CTA is the `.btn-onair` tally fill (see the Marketing exception
  under Colour).
- Below the fold: **the product**, either a live meter or a real screenshot

The hero is the split diagram: one feed goes out as untouched video plus a mix
per destination. It is the one thing no competitor's landing page can show
honestly, because none of them route audio per destination. The meter proof
lives on `/features`.

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
- `scripts/sync-design-tokens.mjs` — how the website gets them
- `ui/src/lib/design-tokens.test.ts` — what stops the two drifting apart again
- [ARCHITECTURE](ARCHITECTURE.md)
- [MONITORING](MONITORING.md) — what the signal colours mean operationally
