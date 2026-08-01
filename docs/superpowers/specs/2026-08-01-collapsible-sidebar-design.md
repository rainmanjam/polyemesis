# Collapsible sidebar design

**What:** the left navigation collapses to an icon-only rail and expands again,
on desktop, remembered between visits.

**Why:** polish rather than pressure. It was asked for as a common affordance the
product does not have, not to solve a measured problem with screen width — which
is the whole reason the design below refuses to auto-collapse at any breakpoint.
An automatic behaviour would be solving a problem nobody reported.

## What exists today

`ui/src/components/AppLayout.tsx`, 241 lines, holds the entire app shell.

- The sidebar is `w-44`, always visible at `md:` and up.
- Below `md` it is an off-canvas drawer, translated in and out by a hamburger in
  the top bar (`md:hidden`), labelled `chrome.toggleNav`.
- Every nav entry already renders `<Icon className="h-3.5 w-3.5 shrink-0" />`
  followed by its label. **The icon half of this feature already exists**; what
  is missing is a width and somewhere to put the label.
- `ui/src/components/ui/tooltip.tsx` exists.
- `ui/src/lib/i18n.ts` is the only current consumer of `localStorage`.

## Scope

**Desktop only**, `md:` and up.

Below `md` the drawer already solves this, and collapsing something already
hidden means nothing. The two mechanisms never coexist: the hamburger is
`md:hidden`, the collapse control is `hidden md:flex`.

## State and persistence

One boolean in `AppLayout`, persisted to `localStorage` under
`polyemesis.nav.collapsed`.

It follows `lib/i18n.ts` exactly, **including its `try`/`catch`**, and that file
already says why:

> Safari in private mode throws on any localStorage access. English is a working
> default, so storage being unavailable must not break the app.

The same reasoning applies with the same force: an unavailable store degrades to
expanded, never to a broken shell. Read once on mount.

**No cross-tab sync.** Two tabs disagreeing about a sidebar width is not a bug
worth the `storage` event listener it would take to fix.

## Collapsed rendering

`w-44` becomes `w-12`. The icon already carries `shrink-0`, so it needs no
change.

**The label stays hidden with `display:none`, and the link also carries an
`aria-label`.** The original version of this section argued for `display:none`
over `sr-only` on the grounds that a visually-hidden label "would make the
collapsed rail read identically to the expanded one" — as if that were a
reason to avoid it. **That reasoning was wrong, and has been reversed.** Reading
identically non-visually is exactly what should happen: the rail is a purely
visual affordance, and a screen-reader user gets no benefit from the nav
losing its names just because it lost its width. The bug this shipped —
fourteen unnamed `link` entries in the accessibility tree, found by driving a
real browser — is the direct cost of the original reasoning.

The fix keeps the visual side unchanged (`display:none` via `md:hidden`, still
never `sr-only` — the rail must stay icon-only to look at) and separately
supplies the accessible name via `aria-label={t(labelKey)}` on the `NavLink`
itself, using the same `t(labelKey)` call the visible label and the tooltip
already use, so the three can never disagree. The name no longer depends on
which CSS is currently hiding the label.

It cannot simply be dropped from the JSX either, because the drawer below `md`
keeps its labels while collapsed — so the condition is the breakpoint AND the
state.

Each entry gains a `Tooltip` carrying its label, from the same `t(labelKey)`
call the expanded nav uses — one source, so the two can never disagree.

**The tooltip is attached only while collapsed.** Expanded, the label is already
on screen and a tooltip repeating it is noise that also delays every hover.

**This is the only reason collapsing is acceptable at all.** An icon-only rail with no
labels anywhere is a memory test, and the icons here are not all
self-evident: Routing, Renditions and Playout are three different pipeline
stages.

## Controls

Two entry points, one state. Neither knows about the other; both call the same
setter.

**Footer chevron.** Pinned to the bottom of the nav, `hidden md:flex`. Points
left when expanded, right when collapsed. It sits with the thing it controls and
stays in place across the transition, so the target does not move under the
cursor.

**Ctrl/Cmd + B.** The convention VS Code, Slack and Notion use for this exact
action.

This is **the app's first global keyboard shortcut**. Every existing handler is
element-scoped — the chat composer, the anchor grid, the confirm dialog, the
upload dropzone. So it introduces a pattern, and the guard is a requirement
rather than a nicety:

> The listener ignores the event when the target is an `input`, `textarea`,
> `select`, or anything `contenteditable`.

polyemesis has a chat panel. Someone typing `Cmd+B` mid-message must not lose
their sidebar, and a product where that happens once is a product where the
shortcut gets disabled.

**Below `md` the shortcut is a no-op in appearance, and that is deliberate rather
than unhandled.** The listener is registered at every width and the state does
change, but only the `md:` rendering reads it — so pressing it on a narrow
viewport sets a preference that takes effect when the window widens. The
alternative, suppressing the key by viewport, means the same keystroke does
different things at different widths, which is harder to explain than a
preference that is simply not visible yet.

## Internationalisation

**No new keys.** The control reuses `chrome.toggleNav`, which already exists in
all fifteen locales and already means precisely this:

| | |
|---|---|
| en | Toggle navigation |
| fr | Afficher/masquer la navigation |
| de | Navigation umschalten |
| ja | ナビゲーションの表示/非表示を切り替え |
| zh-Hans | 切换导航 |

Sharing a label with the mobile hamburger is correct rather than lazy: they are
the same affordance at different viewport sizes and never appear together.

This also avoids the alternative, which was to invent a key and fill fourteen
locales with English text dressed as translations — passing the locale drift
guard while lying to every operator who does not read English.

## Accessibility

- The footer chevron button: `aria-label={t("chrome.toggleNav")}` and
  `aria-expanded`. **Not the `<nav>`.** An earlier version of this section put
  `aria-expanded` on the `<nav>` too, reasoning that the collapsed rail is a
  different presentation of the same landmark. That is not a supported ARIA
  state for `role="navigation"` (the implicit role of `<nav>`) — only widgets
  such as buttons support `aria-expanded` — so it was removed from the `<nav>`
  and kept only on the button, which is the control the state actually
  describes.
- Each `NavLink` carries `aria-label={t(labelKey)}`, independent of collapsed
  state, so the accessible name never depends on which CSS is currently
  hiding the visible label (see "Collapsed rendering" above).
- The `<nav>` keeps its landmark role at both widths.
- Focus order is unchanged; collapsing removes text, not tab stops.

## Testing

| Case | Why it matters |
|---|---|
| Collapsed nav renders icons and no visible label | The display:none decision, asserted rather than assumed |
| Tooltip carries the label the expanded nav shows | The two must come from one source |
| `localStorage` throwing → expanded, no throw | The Safari private-mode path `i18n.ts` already documents |
| Shortcut fires from `document.body` | The feature works |
| Shortcut does NOT fire from a focused input | The chat-composer case |
| The existing i18n guard stays green | Proves no key was added |

## Deliberately not built

- **No auto-collapse at any breakpoint.** The motivation was polish, not screen
  pressure. An automatic behaviour would be answering a question nobody asked and
  would be felt as the layout moving on its own.
- **No per-page memory.** One preference, not fourteen.
- **No animation beyond a width transition.**
- **No cross-tab sync.**
- **No change to mobile.** The drawer is untouched.

## Risks

**`AppLayout.tsx` is the app shell, so a mistake here is visible on every page.**
That argues for the change being small and for the tests above being real rather
than a formality.

**The first global key listener sets a precedent.** Whatever guard this ships
with is the guard the next shortcut will copy, so it is written to be copied:
one predicate, named, with the reason in a comment.

**The tooltip is load-bearing.** If it fails to render, the collapsed rail
becomes unusable rather than merely worse. It is a shipped primitive already used
elsewhere, which is the mitigation, but it is worth knowing the failure is not
graceful.
