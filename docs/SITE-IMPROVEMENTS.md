# polyemesis website — measured findings and recommended changes

Assembled 2026-08-13 from four independent reviews (information architecture,
copy, layout, and a comparable-product pattern survey), plus direct measurement
of the live site and the built output.

**Every claim here carries a number that was measured.** Where a review asserted
something that measurement contradicted, the measurement wins and the discrepancy
is written down — there are three such cases below, and they are the most useful
entries in the document.

---

## 0. Already fixed — do not re-report

These were found during the review and are merged or in review. Listed so the
next pass does not spend effort on them again.

| Fix | Was | Now |
|---|---|---|
| Whole page scrolled sideways on a phone | `/download` 630px, `/comparison` 570px against a 375px viewport | 375px, both |
| Comparison row label scrolled away from its Yes/No cells | first column `position: static` | pinned, opaque `--color-ink` |
| Nav marked no page as current, on every page | `Astro.url.pathname` is `/features.html`, href is `/features` | normalised; both navs mark, home marks nothing |
| Footer column labels polluted every heading outline | `<h2>` | `<p>` |
| No version anywhere on the site | — | footer, read from `git describe` at build time |
| Site never said who it is *not* for | — | "Is this for you?" with a not-for column |
| Screenshots a week stale, 18 UI commits behind | 2026-08-06 | re-shot; `docs/media` 4013K→1587K, site 1819K→707K |
| `og:image` had no dimensions or alt | — | added, stating the real 2560×1280 |

Three of these are guarded in `web/scripts/check-build.mjs` and each guard was
mutation-tested. The nav guard asserts **two** marked anchors per page rather
than one, because one would have passed the half-fix that left the mobile menu
blank.

---

## 1. Confirmed defects, not yet fixed

### 1.1 `/features` duplicates the entire comparison table — High, S
**Measured:** 9 of 9 rows are *character-identical* to the primary table on
`/comparison`. Not a summary, not an excerpt — the same table.

The cost is not bytes, it is authority. A visitor who reads it on `/features`
and meets it again on `/comparison` learns the second page has nothing new,
which is the wrong lesson about the page whose whole job is the comparison.

**Fix:** on `/features`, replace it with a three-line callout — the single
strongest row plus a link to `/comparison`. Keep the full table on the page
named for it.

### 1.2 `/docs` is a link farm off the site — Medium, M
**Measured:** 48 links, 25 to github.com, **17 of them straight to raw `.md`
files** in the repo.

Every one of those is an exit. The visitor lands on GitHub's markdown renderer —
no nav, no search, no way back, and none of the site's typography. For a project
whose adoption depends on someone successfully installing it, the documentation
funnel leaks at the exact moment of highest intent.

**Fix:** render the quickstart and configuration pages on-site from the same
markdown, via an Astro content collection. Reserve off-site links for reference
material that genuinely lives in the repo (`CHANGELOG`, `LICENSE`, issues).

### 1.3 Claims on `/` and `/comparison` carry no source — Medium, M
Flagged by the copy review as its top category. The specific strings:

- *"CPU cost is nearly independent of resolution"* — a performance claim with no
  benchmark, workload, or hardware named.
- *"it is the path that is operated daily"* — a credibility claim with no
  release cadence or test record behind it.
- *"What only polyemesis does"* — an exclusivity claim against named competitors
  with no dated source beside any cell.

This audience checks. An unsourced comparison against named products is the
fastest way to lose a reader who would otherwise have installed the thing.

**Fix:** put a dated primary-source link in each competitor cell, and either
benchmark the CPU claim or soften it to what the architecture actually
guarantees (video is stream-copied, so it is not re-encoded per destination —
which is a *structural* statement and needs no benchmark).

---

## 2. Where a review was wrong, and what measuring showed instead

Recorded because the pattern repeats: a review reports a real symptom and
prescribes a fix for a cause that is not the actual one.

| Reported | Prescribed | What was actually true |
|---|---|---|
| "Active nav links render in identical `rgb(155,169,186)`" | "Apply `aria-current` and high-contrast styling" | `aria-current` and `text-fg` were **already there**. The input was wrong: flat-file output makes `pathname` `/features.html`, so the comparison was false on every page. |
| "Downscale screenshots to 1440px, 73.5% saving" | resize + quantise | Resizing makes PNG compression **worse** here. At 2304px the saving fell to 42.6% and `02-routing.png` came out **19% larger** than full-res. Quantise, do not resize. |
| (my own first fix) sticky column with `var(--color-bg)` | — | That token does not exist in this theme. It resolved to transparent while `position: sticky` and `stayedPinned: true` both read as correct. The mechanism was right and the thing it exists to do was broken. |
| "8 of 12 grids skip the tablet band" — add the missing step to each | `md:grid-cols-*` throughout | True for the count, wrong for the fix on most of them. Nine are a text column beside a screenshot, and there the **single column at 768 is the better layout**: stacked, the shot gets 726px; the desktop two-column arrangement gives it 688px. Only `/download` was changed. |
| "Body measure exceeds 85ch — docs has 16 rows at 101ch" | narrow the reading column | **Zero** actual prose paragraphs exceed 85ch. The 101ch elements are `<li>`s wrapping card content — a title, a filename and a description stacked — not reading columns. The guideline does not apply to them. |
| (my own tablet fix) wrap rule below a 40rem viewport | — | What decides whether a command fits is its **container**, not the window. The moment `/download` had two columns, the block sat in a 293px column at a 768px viewport: too wide for the query, far too narrow for the command. 265px hidden again, worse than before the fix. |

---

## 3. Layout and rhythm

### 3.1 `/features` is a conveyor belt — Medium, M
**Measured:** sections `#routing`, `#matrix`, `#renditions`, `#monitoring`,
`#recording`, `#chat` are each **exactly 593px tall** on desktop. Six identical
blocks in a row give the eye no focal point and no sense of which capability
matters most.

**Fix:** give the differentiator (`#routing`, `#matrix`) more vertical room and a
distinct treatment; compress the four supporting ones. Uniformity here reads as
"six equally minor features", which undersells the one that is not.

### 3.2 The landing page is 8918px tall at 390px — Low, M
**Measured:** 22 phone screens. `WHAT ELSE IT DOES` alone is 1454px and sits
between the proof section and the architecture section, so a technical reader
scrolls a long way past secondary material to reach the part that would convince
them.

**Fix:** compress the six secondary features into a 3×2 grid and move
`HOW IT IS BUILT` up, directly after the proof.

### 3.3 Nothing that scrolls or expands says so on touch — Medium, S
Two findings with one cause, and it is the class of bug that desktop review
never catches: **every affordance on this site is expressed as a hover or a
cursor**, and a phone has neither.

- **The lightbox is undiscoverable.** `.shot-open` communicates itself with
  `cursor: zoom-in`. On touch that is nothing at all. Measured at 390px, each
  2880×1800 capture renders at **348×218** — an 8.28× reduction per dimension,
  which is precisely where a reader most needs to expand it and has no way to
  learn they can.
- **The comparison tables give no sign they scroll.** The visible frame shows
  **348 of 736px — 47%** of the table. macOS and iOS both hide overlay
  scrollbars until a scroll is already in progress, so the reader has to guess
  that the other 53% exists.

**Fix:** a persistent expand glyph in the corner of each screenshot rather than
a cursor change, and a fade or edge shadow on the table wrapper that says
content continues past the right edge. Both are visual, both work without
pointer events.

### 3.4 The 768px layout is longer than desktop — Low, M
**Measured:** `/features` is **7527px** at 768px against **5576px** at 1440px.
All six text/image pairs stack into single columns at tablet width, so each
becomes 853–943px tall. The tablet reader scrolls 35% further than the desktop
reader for identical content.

### 3.5 Screenshot resolution is correct — do not "optimise" it
**Measured:** inline shots render at 688 CSS px at a 1440 viewport; the lightbox
expands them to **1152 CSS px**, which at dpr 2 needs 2304. They are 2880 wide.

A future performance audit will recommend downscaling these. It will be wrong
for the reason in §2 — and it will also blur the lightbox, which is the view the
screenshots exist to provide.

---

## 4. Patterns worth adopting, from comparable products

From a survey of adjacent categories on Mobbin. Honest gap up front: **the
streaming/broadcast category returned no domain peers** — no Restream, Castr,
OBS or Owncast in the library. The applicable patterns come from developer
tools, open-source infrastructure and self-hosted products instead.

| # | Change | Why | Effort |
|---|---|---|---|
| 1 | **Say the matrix is interactive.** A visible affordance next to it, not prose. | The copy currently says "Light a crosspoint" and "Hover a column" — buried in a paragraph, in jargon. It is the only interactive thing on the site and the strongest asset it has. | S |
| 2 | **License eyebrow + copyable install line in the hero.** `MIT · SINGLE BINARY · SELF-HOSTED` above the H1, the install command beside the CTA. | The two facts this audience decides on are licence and how hard it is to run. Both are currently below the fold. | S |
| 3 | **Annotate the spectrograms.** Leader lines marking the band that actually differs between the two mixes. | The proof section shows two images and asks the reader to spot the difference. Naming it converts a picture into evidence. | S |
| 4 | **Add a boxed "Why can't OBS just do this?"** — three sentences, beside the differentiator. | It is the first objection every reader in this audience raises, and the site never answers it directly. | S |
| 5 | **A fan-out diagram in the hero** (one source → hub → many destinations, each with a different mix). | Topologically identical to Customer.io's, and it states the product's whole thesis without prose. | M |

### Deliberately rejected
Common in SaaS marketing, actively harmful for a self-hosted MIT project with
this audience: logo walls, review-site award badges, pricing or plan tables,
"no credit card required", email-gated demos, stock photography, mascots, and
unattributed testimonials. A quote reading "— Streaming Engineer" costs more
credibility than it buys. Supabase's quote carousel works *only* because every
quote carries a real linked handle.

### Already right — do not break
Measured and confirmed consistent across all pages and all three viewports:

- **The grid.** Every page uses a 1280px wrap starting at x=80, content aligned
  to x=100, and the side inset stays consistent down to 390px.
- **The type scale.** H1 48px desktop and tablet, 36px mobile (home 54.4px);
  body 16px/24px; lead 18px. One system, applied everywhere.
- **The screenshot treatment.** Feature captures are 688×430 at 1440px — 1.60:1,
  matching the source aspect ratio exactly, in a single aligned column.
- **The docs split.** 208px side nav, 64px gutter, 968px reading column, folding
  to one column at 390px with no overflow.

And, from the pattern survey: the interactive matrix; the spectrogram proof
section; the problem-before-solution order; the for-you/not-for-you list; dark
developer-tool styling; the absence of a pricing page; the 404 page written in
the product's own vocabulary; and a footer that deep-links to sections rather
than cloning the header.

---

## 5. Suggested order

Ranked by (severity × reach) ÷ effort.

1. **§1.1** de-duplicate the comparison table on `/features` — S, and it is a deletion
2. **§3.3** touch affordances for the lightbox and the scrolling tables — S, and it is the only category here that makes a working feature *reachable* rather than making an existing one nicer
3. **§4.1–4.4** the four S-effort marketing wins — each is one small block
4. **§1.3** source the competitive claims — the credibility risk with this audience
5. **§3.1** break the 593px conveyor belt on `/features`
6. **§1.2** bring the docs on-site — largest effort, largest funnel payoff
7. **§3.2, §3.4** compress and reorder the landing page; fix the tablet stack

Items 1–3 are roughly an afternoon together and touch nothing structural.

---

## 6. Method

Four reviewers, deliberately given different briefs so they would not converge:
information architecture, copy, layout, and a comparable-product pattern survey.
Findings were then re-measured directly before being written down — which is how
the three corrections in §2 were caught.

One reviewer confirmed the mobile fixes independently by measuring the live
Worker against the local build in the same run: live computed
`.card { min-width: auto }` with `.cmp` cells `static`, local computed `0px` and
`sticky`. That is the deploy gap, not a disagreement.

A fifth review (layout, second opinion) did not return in time; a sixth was
refused for quota. Neither gap affects the findings above, all of which are
measured rather than reported.
