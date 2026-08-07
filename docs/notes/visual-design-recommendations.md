# Style, animation and colour: what to add, and what to fix first

Four independent reviews — codex (operator app), agy (marketing site), and two
Opus agents (colour system, motion system) — against the real repo, not a
description of it. Every claim below that touches this codebase was checked
before it was written down; the ones that were checked and **failed** are marked,
because two of them were mine.

## The finding that reframes the request

The ask was "more style, animation and colour". Three of those are available.
**Colour is not** — not in the app.

Six of the seven usable hue families already carry meaning:

| hue | token | meaning |
|---|---|---|
| 147° | `--live` | on air |
| 105° | `--meter-mid` | approaching peak |
| 75° | `--warn` | reconnecting / degraded |
| 28° | `--down` | failed |
| 225° | `--armed` | armed, not yet live |
| 281° | `--primary` | interactive |

The only unclaimed band is **~300–360°** (magenta/violet). Anything decorative
outside it reads as a state, in a tool where a colour is a claim about whether a
broadcast is up.

So richness in the app has to come from the non-hue axes — **shape, elevation,
density, typography, motion** — plus that one free band on the marketing site,
which has no live state to misreport. The website can take colour freely; the
app cannot.

## Fix before adding

These are defects found while looking for opportunities. Adding polish on top of
them makes the app worse, not better.

| # | Defect | Evidence | Where |
|---|---|---|---|
| F1 | **The reconnecting dot fades itself.** `live` pulses a *separate halo* while its core stays solid; `warn` pulses **the core**, dropping the only indicator to `opacity: 0.35` twice a second. The most urgent state is the one that periodically becomes hardest to see. | Read directly | `signature/StatusDot.tsx:19-29`, `index.css:239-254` |
| F2 | **The motion tokens are dead.** `--motion-instant/quick/settle` + `--ease-out` are declared in `:root` and registered in `@theme inline` **zero** times, used as utilities **zero** times. All 27 `transition-colors` use Tailwind's default. The reduced-motion block sets those tokens to `0ms` — and nothing reads them. | 6 declared / 0 registered / 0 used | `ui/src/index.css:99-108, 117-186` |
| F3 | **Reduced motion deletes a state channel.** Blink rate is the only non-hue signal separating *reconnecting* from *live*. The blanket `animation-iteration-count: 1` removes it. It escapes being *invisible* only by luck — `signal-pulse`'s 100% keyframe is `opacity: 1`. This is the website's scroll-reveal bug in a different key. | Read directly | `ui/src/index.css:260-274` |
| F4 | **Twenty raw Tailwind colours collide with the semantic tones**, and the guard is blind to them: `theme.test.ts` whitelists the whole Tailwind palette. `red-500` is ΔE **8.11** from `--down` — a second red meaning something else. | 20 uses, 6 files | `AutomodMatrix`, `DestinationDialog`, `Dashboard`, `SettingsPage`, `RenditionsPage`, `PublicPlayer` |
| F5 | **Status text fails WCAG AA.** `idle` (`--subtle-foreground` #5d6779) is **2.89:1** on `card-raised` — the *Offline* tone. `down` badge text is **3.88:1**. `primary` is **4.49:1**. | Computed, two independent passes agreed | `ui/src/index.css` |
| F6 | **Colour-blindness collapses the ramp.** Deuteranopia: `live`→#bfb174 vs `down`→#9b8d48, ΔE **14.58** — on-air and failed both go olive. Meter `low`→`peak` ΔE 14.58. ~8% of male operators. | Machado 1.0 / ΔE76 | `StatusDot`, `AudioMeter` |
| F7 | **The meter gradient is rebuilt every frame, per meter.** `buildGradient` is called inside the rAF draw loop. Up to 64 channels. | Read directly | `signature/AudioMeter.tsx:153` |

## Recommendations, ranked by value to effort

Source: **CX** codex · **AG** agy · **MO** motion agent · **CO** colour agent · **✔** verified against the code.

| # | Area | Recommendation | Where | Why | Effort |
|---|---|---|---|---|---|
| 1 | Motion · app | Move the `warn` pulse off the core dot onto a halo, matching `live`. Core stays 100% opaque. | `StatusDot.tsx` | Fixes F1. The urgent state stops hiding. | S · CX ✔ |
| 2 | Motion · both | Register the existing tokens in `@theme inline`, then use `duration-(--motion-instant)` at call sites. Add the same scale to the website, which has **zero** named motion tokens. | `index.css`, `web/global.css` | Fixes F2 and makes reduced-motion actually work. | S · MO ✔ |
| 3 | Colour · app | Give each tone a **shape**, not just a fill: live = disc, warn = disc + ring, down = square/×, armed = hollow ring, idle = dashed ring. | `StatusDot.tsx` | Fixes F6 with no added clutter. Survives CVD *and* greyscale. | M · CO |
| 4 | Motion · app | Reduced motion must **replace, not remove**, a meaning-carrying cue: keep a static ring on `warn` when the blink is suppressed. | `index.css:260-274` | Fixes F3. | S · MO+CO |
| 5 | Colour · app | Promote `idle` to its own `--idle: #7b8698` (≈4.6:1); lighten `--down` to #f0656a, keep #e5484d as `--down-strong` for fills. | `index.css`, `signal.ts` | Fixes F5 on the two most operationally loaded states. | S · CO |
| 6 | Guard | Make `theme.test.ts` **fail** on raw `amber-*`/`red-*`/`green-*`, and add a contrast assertion per tone × surface. | `theme.test.ts` | Fixes F4 and stops the next instance. The current guard proves a token exists, not that it is readable. | M · CO ✔ |
| 7 | Style · web | Replace smooth sine meter loops with **asymmetric attack/decay + peak-hold ticks**, and add an EBU R128 graticule (−40/−23/−16/0). | `LoudnessBars.astro`, `global.css` | Engineers spot fake web animation instantly. Real ballistics buy domain trust. | S · AG |
| 8 | Style · web | Hardware affordances over SaaS polish: bevelled crosspoint switches, tactile `active:translate-y-[1px]`, hard 2px focus rings, `MOD-01…06` spec pills. | `global.css` `.xpt`/`.btn`, `index.astro` | The audience distrusts polish; a patchbay reads as competence. | S · AG |
| 9 | Style · app | Failure hierarchy by **rail, not fill**: a 2px left rail in the semantic tone. Never colour a whole card. | `DestinationCard.tsx:96` | Scannable from a second monitor without becoming a traffic light. | S · CX |
| 10 | Style · app | Emphasise only *abnormal* metrics (`Dropped > 0`, `Speed < 0.95`). Healthy values stay neutral. | `DestinationCard.tsx:180-217` | Turns five equal numbers into an exception map. | M · CX |
| 11 | Colour · web | Decorative colour is allowed **only** in the 300–360° magenta-violet band. | `web/src/styles/global.css` | The one hue with no assigned meaning. The site has no live state to misreport. | S · CO |
| 12 | Perf | Cache the gradient per width; share one rAF clock across meters; drop to 10 Hz when all channels are at floor. | `AudioMeter.tsx`, new `useMeterClock.ts` | Fixes F7. 64 channels × 60 Hz is the whole frame budget. | M · MO ✔ |

## Never animate

Converged on independently by codex and the motion agent:

- **Measured values** — bitrate, dropped frames, speed, LUFS, restarts, uptime.
  Tweening 4200→4600 kbps renders numbers that were never true, on the readout
  an operator escalates from.
- **The meters.** The rAF loop is the only motion authority; a CSS transition on
  top shows a level that lags the audio.
- **State colour.** A destination that dropped must read red on the frame it
  dropped. Easing a status colour is the UI asserting something false for 160ms.
- **Card reorder and removal.** An animated reorder slides the click target out
  from under a cursor mid-show.

One rule for the top of both stylesheets:

> Motion explains a state change or reports a state. If it does neither, it is
> competing with the meters.

## Corrections made during this review

- My own brief said the app has dark **and light** mode. It does not —
  `index.css:18` says so explicitly, and light mode is planned for 0.4.0 (#97).
  The colour agent checked rather than trusting the brief. Every "light mode"
  number here is therefore a projection, and it is a blocking one: the current
  signal hexes are 1.6–2.4:1 on white and **cannot be reused**.
- agy's first two runs produced nothing usable — once from a permissions denial,
  once because `-p` consumed the following flag as its prompt. Recorded because
  the failure was silent both times and looked like a model result.
