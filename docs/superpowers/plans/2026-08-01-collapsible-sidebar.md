# Collapsible Sidebar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The left navigation collapses to an icon-only rail and expands again, on desktop, remembered between visits.

**Architecture:** One boolean in `AppLayout`, persisted to `localStorage`, read by the sidebar's width and by each entry's label. Two controls write it: a footer chevron and the app's first global key listener. Nothing else in the tree knows the state exists.

**Tech Stack:** React 19, Tailwind, `lucide-react` icons, Radix tooltip via `ui/tooltip.tsx`, Playwright for tests. **No new dependencies.**

## Global Constraints

- **No new i18n keys.** The control reuses `chrome.toggleNav`, which exists in all fifteen locales. `internal/web/i18n_drift_test.go` fails the build if a key is added to `en.json` and not the other fourteen.
- **Desktop only.** The collapse control is `hidden md:flex`; the mobile hamburger is `md:hidden`. They never appear together and the drawer is untouched.
- **`localStorage` access is wrapped in `try`/`catch`.** Safari in private mode throws on any access, and `ui/src/lib/i18n.ts:92` already documents this. Unavailable storage degrades to expanded, never to a broken shell.
- **The label must never be `sr-only` when collapsed.** `display:none` — which is what Tailwind's `md:hidden` compiles to — removes it from the accessibility tree, and that is correct. A *visually-hidden* label (`sr-only`, clip-path) stays in the tree and would make the collapsed rail read identically to the expanded one, with the tooltip as redundant noise on top. The distinction is which CSS, not CSS versus JSX.
- **The tooltip attaches only while collapsed.**
- **There is no UI unit-test framework.** No vitest, no jest, no testing-library. Tests go in `ui/e2e/polyemesis.spec.ts` alongside the existing `navigation` and `i18n` blocks, and run under `./scripts/acceptance-browser.sh`.
- CI gates, in CI's order: `cd ui && npx tsc -b --noEmit`; `npm run lint`; `npm run build`. Go is untouched by this plan, but `go test ./internal/web/` must stay green because the i18n guard reads the locale files.
- British spelling in prose. Comments explain *why*, and name the failure that motivated the decision.

---

### Task 1: The persisted boolean

**Files:**
- Create: `ui/src/hooks/useNavCollapsed.ts`

**Interfaces:**
- Consumes: nothing.
- Produces: `useNavCollapsed(): [boolean, () => void]` — the current state and a toggle. Task 2 and Task 3 both call it.

Extracted into its own file rather than inlined in `AppLayout` because it is the only part of this feature with logic worth reading on its own, and because `AppLayout.tsx` is already 241 lines holding the whole shell.

- [ ] **Step 1: Write the hook**

Create `ui/src/hooks/useNavCollapsed.ts`:

```ts
import { useCallback, useEffect, useState } from "react";

/** Where the preference lives. Namespaced like `polyemesis.language`, which is
 *  the only other key this app stores. */
const STORAGE_KEY = "polyemesis.nav.collapsed";

/** Reads the stored preference, defaulting to expanded.
 *
 *  Wrapped because Safari in private mode throws on ANY localStorage access --
 *  see the same guard and the same reasoning in lib/i18n.ts. A sidebar
 *  preference is not worth breaking the app shell over, and expanded is a
 *  working default.
 */
function readStored(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

/** The collapsed state of the desktop sidebar, and a toggle for it.
 *
 *  Read once on mount rather than subscribed to: two tabs disagreeing about a
 *  sidebar width is not a bug worth a `storage` listener.
 */
export function useNavCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(readStored);

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_KEY, String(collapsed));
    } catch {
      // Nothing to do and nothing to report: the preference simply does not
      // survive this session, which is the documented Safari behaviour.
    }
  }, [collapsed]);

  const toggle = useCallback(() => setCollapsed((v) => !v), []);
  return [collapsed, toggle];
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd ui && npx tsc -b --noEmit`
Expected: clean. Nothing imports it yet, which is fine — the next task does.

- [ ] **Step 3: Commit**

```bash
git add ui/src/hooks/useNavCollapsed.ts
git commit -m "feat(ui): the sidebar's collapsed state, persisted

One boolean, read once on mount and written on change, wrapped in the same
try/catch lib/i18n.ts uses -- Safari in private mode throws on any localStorage
access, and a sidebar preference is not worth breaking the shell over.

No storage listener: two tabs disagreeing about a sidebar width is not a bug
worth the code to fix."
```

---

### Task 2: The collapsed rail

**Files:**
- Modify: `ui/src/components/AppLayout.tsx` (the `{/* ---- sidebar ---- */}` block, and the import list)

**Interfaces:**
- Consumes: `useNavCollapsed` from Task 1.
- Produces: a sidebar that renders at `w-12` with no label text when collapsed. Task 3 adds the second control; Task 4 tests both.

- [ ] **Step 1: Add the imports**

In `ui/src/components/AppLayout.tsx`, add to the existing import block:

```ts
import { useNavCollapsed } from "@/hooks/useNavCollapsed";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
```

and add `ChevronLeft` and `ChevronRight` to the existing `lucide-react` import.

- [ ] **Step 2: Call the hook**

Immediately after `const [mobileOpen, setMobileOpen] = useState(false);` — `ui/src/components/AppLayout.tsx:82` — add:

```ts
const [navCollapsed, toggleNav] = useNavCollapsed();
```

- [ ] **Step 3: Replace the sidebar block**

Replace the whole `<nav>…</nav>` block with:

```tsx
        {/* ---- sidebar ---- */}
        <TooltipProvider delayDuration={0}>
          <nav
            aria-expanded={!navCollapsed}
            className={cn(
              "z-40 flex shrink-0 flex-col gap-0.5 border-r border-border bg-background p-2 transition-[width]",
              navCollapsed ? "md:w-12" : "md:w-44",
              // The drawer is always full width: a collapsed rail behind a
              // hamburger would be an icon strip nobody asked for, and the
              // drawer already solves the problem collapsing solves.
              "w-44",
              "max-md:absolute max-md:inset-y-11 max-md:left-0 max-md:transition-transform",
              mobileOpen ? "max-md:translate-x-0" : "max-md:-translate-x-full",
            )}
          >
            {NAV.map(({ to, labelKey, label, icon: Icon, end }) => {
              const text = labelKey ? t(labelKey) : label;
              const link = (
                <NavLink
                  key={to}
                  to={to}
                  end={end}
                  className={({ isActive }) =>
                    cn(
                      "flex items-center gap-2 rounded-md px-2 py-1.5 text-[12px] transition-colors",
                      navCollapsed && "md:justify-center md:px-0",
                      isActive
                        ? "bg-primary-dim text-foreground"
                        : "text-muted-foreground hover:bg-accent hover:text-foreground",
                    )
                  }
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                  {/* md:hidden, which compiles to display:none and therefore
                      leaves the accessibility tree. NOT sr-only: a
                      visually-hidden label stays in the tree, which would make
                      the collapsed rail read identically to the expanded one
                      and the tooltip redundant noise on top of it.

                      Conditioned on the breakpoint AND the state, because the
                      drawer below md keeps its labels even while collapsed. */}
                  <span className={cn(navCollapsed && "md:hidden")}>{text}</span>
                </NavLink>
              );

              // Only while collapsed. Expanded, the label is already on screen
              // and a tooltip repeating it is noise that also delays every hover.
              if (!navCollapsed) return link;
              return (
                <Tooltip key={to}>
                  <TooltipTrigger asChild>{link}</TooltipTrigger>
                  <TooltipContent side="right">{text}</TooltipContent>
                </Tooltip>
              );
            })}

            {/* Pinned to the bottom, so the target does not move as the width
                changes. mt-auto rather than a spacer div. */}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={toggleNav}
              aria-label={t("chrome.toggleNav")}
              aria-expanded={!navCollapsed}
              className="mt-auto hidden self-end md:flex"
            >
              {navCollapsed ? <ChevronRight /> : <ChevronLeft />}
            </Button>
          </nav>
        </TooltipProvider>
```

- [ ] **Step 4: Verify it compiles and builds**

Run: `cd ui && npx tsc -b --noEmit && npm run lint && npm run build`
Expected: all three clean.

- [ ] **Step 5: Look at it**

Run: `cd ui && npm run dev`, open the app, and check by hand:
- The chevron collapses the rail to icons and expands it again.
- Hovering a collapsed icon shows its label.
- Reloading keeps the state.
- Narrowing the window below `md` shows the drawer, still with labels.

This step is manual because there is no UI unit-test framework; Task 4 adds the automated version.

- [ ] **Step 6: Commit**

```bash
git add ui/src/components/AppLayout.tsx
git commit -m "feat(ui): collapse the sidebar to an icon-only rail

Desktop only. Below md the drawer already solves this, and it keeps its labels
at every width -- a collapsed rail behind a hamburger would be an icon strip
nobody asked for.

The label is hidden with display:none, never sr-only. The distinction is which
CSS: display:none leaves the accessibility tree, a visually-hidden label does
not -- and an sr-only label would make the collapsed rail read identically to the
expanded one to a screen reader, with the tooltip as redundant noise on top.

It is conditioned on the breakpoint as well as the state, because the drawer
below md keeps its labels while collapsed.

The tooltip attaches only while collapsed, for the same reason inverted: the
label is already on screen when expanded.

No new i18n key -- chrome.toggleNav already exists in all fifteen locales and
already means exactly this."
```

---

### Task 3: The keyboard shortcut

**Files:**
- Modify: `ui/src/hooks/useNavCollapsed.ts`

**Interfaces:**
- Consumes: the `toggle` from Task 1.
- Produces: nothing new. `useNavCollapsed` gains a `useEffect`; its signature is unchanged, so `AppLayout` needs no edit.

Put here rather than in `AppLayout` so the whole feature's behaviour lives in one file and the shell only renders.

- [ ] **Step 1: Add the typing guard and the listener**

Append to `ui/src/hooks/useNavCollapsed.ts`, above `useNavCollapsed`:

```ts
/** Whether a keystroke belongs to something the user is typing into.
 *
 *  This is the app's FIRST global key listener -- every other handler is
 *  element-scoped, on the chat composer, the anchor grid, the confirm dialog and
 *  the upload dropzone. So this predicate is the pattern the next shortcut will
 *  copy, and it is written to be copied.
 *
 *  polyemesis has a chat panel. Someone typing Cmd+B mid-message must not lose
 *  their sidebar, and a product where that happens once is a product where the
 *  shortcut gets turned off.
 */
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  return ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
}
```

and inside `useNavCollapsed`, after the persistence effect:

```ts
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // metaKey for macOS, ctrlKey elsewhere. The browser binds neither to
      // anything on a page with no rich-text editing.
      if (e.key.toLowerCase() !== "b" || !(e.metaKey || e.ctrlKey)) return;
      if (isTyping(e.target)) return;
      e.preventDefault();
      setCollapsed((v) => !v);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
```

- [ ] **Step 2: Verify it compiles**

Run: `cd ui && npx tsc -b --noEmit && npm run lint`
Expected: clean.

- [ ] **Step 3: Check it by hand, including the guard**

Run `npm run dev` and confirm:
- `Cmd/Ctrl+B` toggles the sidebar from anywhere on the page.
- Focus the chat composer, type `Cmd/Ctrl+B` — the sidebar does **not** move.

- [ ] **Step 4: Commit**

```bash
git add ui/src/hooks/useNavCollapsed.ts
git commit -m "feat(ui): Ctrl/Cmd+B toggles the sidebar

The convention VS Code, Slack and Notion use for exactly this.

It is the app's first global key listener -- every existing handler is
element-scoped -- so the typing guard is a requirement rather than a nicety, and
it is written to be the pattern the next shortcut copies. polyemesis has a chat
panel: someone typing Cmd+B mid-message must not lose their sidebar.

Below md the state changes and nothing looks different, because only the md:
rendering reads it. Suppressing the key by viewport would make one keystroke do
different things at different widths, which is harder to explain than a
preference that is simply not visible yet."
```

---

### Task 4: Pin the behaviour in Playwright

**Files:**
- Modify: `ui/e2e/polyemesis.spec.ts`

**Interfaces:**
- Consumes: the finished feature.
- Produces: nothing other tasks use.

There is no UI unit-test framework in this repo, so these go beside the existing `navigation` and `i18n` blocks, which is where every other UI assertion lives.

- [ ] **Step 1: Write the tests**

Append to `ui/e2e/polyemesis.spec.ts`:

```ts
test.describe("sidebar collapse", () => {
  // The nav is the app shell, so a mistake here is visible on every page. These
  // pin the three decisions that are easy to undo by accident: the label leaves
  // the DOM, the preference survives a reload, and the shortcut does not fire
  // while somebody is typing.

  test("collapsing removes the labels and expanding brings them back", async ({ page }) => {
    await signIn(page);
    await expect(page.locator("nav")).toContainText("Dashboard");

    await page.getByRole("button", { name: "Toggle navigation" }).click();
    // toContainText reads visible text, so this asserts display:none took
    // effect. It would also pass for sr-only, which is why the reasoning lives
    // in the component comment rather than only here.
    await expect(page.locator("nav")).not.toContainText("Dashboard");

    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).toContainText("Dashboard");
  });

  test("the collapsed state survives a reload", async ({ page }) => {
    await signIn(page);
    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).not.toContainText("Dashboard");

    await page.reload();
    await expect(page.locator("nav")).not.toContainText("Dashboard");

    // Put it back, so a later test does not start against a collapsed nav.
    await page.evaluate(() => localStorage.setItem("polyemesis.nav.collapsed", "false"));
  });

  test("the shortcut toggles it, and not while typing", async ({ page }) => {
    await signIn(page);
    await page.goto("/chat");
    await expect(page.locator("nav")).toContainText("Dashboard");

    await page.locator("body").press("ControlOrMeta+b");
    await expect(page.locator("nav")).not.toContainText("Dashboard");
    await page.locator("body").press("ControlOrMeta+b");
    await expect(page.locator("nav")).toContainText("Dashboard");

    // The case the guard exists for. The chat composer is a textarea; the
    // sidebar must not move while somebody is writing a message into it.
    const composer = page.locator("textarea").first();
    await composer.click();
    await composer.press("ControlOrMeta+b");
    await expect(page.locator("nav")).toContainText("Dashboard");
  });
});
```

- [ ] **Step 2: Run them**

Run: `./scripts/acceptance-browser.sh`
Expected: the suite passes, including the three new tests.

If the chat composer is not a `textarea`, read `ui/src/components/ChatPanel.tsx:437` — the existing `onKeyDown` is on the composer — and fix the locator rather than deleting the assertion. That assertion is the reason the guard exists.

- [ ] **Step 3: Mutation-test the guard**

Temporarily delete the `if (isTyping(e.target)) return;` line in `useNavCollapsed.ts`.
Run: `./scripts/acceptance-browser.sh`
Expected: FAIL on "the shortcut toggles it, and not while typing".
Restore the line, re-run, confirm PASS.

**This step is not optional.** A guard whose test cannot fail is a green light with nothing behind it.

- [ ] **Step 4: Correct the stale comment in ci.yml**

`.github/workflows/ci.yml` labels the suite `acceptance-browser  # 24 checks`. Nothing asserts that number — `acceptance-browser.sh` runs `npx playwright test` and exits with its status, and Playwright counts itself — so this is a comment going stale rather than a gate breaking. Raise it by the number of tests added.

- [ ] **Step 5: Commit**

```bash
git add ui/e2e/polyemesis.spec.ts .github/workflows/ci.yml
git commit -m "test(e2e): pin the sidebar collapse behaviour

Three decisions that are easy to undo by accident: the label leaves the DOM
rather than being hidden, the preference survives a reload, and the shortcut
does not fire while somebody is typing.

The last one is mutation-tested: deleting the typing guard fails it. The chat
composer is the case it exists for."
```

---

### Task 5: Verification

- [ ] **Step 1: Every gate CI runs, in CI's order**

```bash
cd ui && npx tsc -b --noEmit && npm run lint && npm run build
cd .. && go test ./internal/web/ -count=1
```

Expected: three UI gates clean; the i18n guard green, which proves no locale key was added.

- [ ] **Step 2: Prove no new i18n key**

```bash
git diff main --stat -- ui/src/lib/i18n
```

Expected: **empty**. The feature reuses `chrome.toggleNav`.

- [ ] **Step 3: Prove no new dependency**

```bash
git diff main -- ui/package.json ui/package-lock.json
```

Expected: **empty**.

- [ ] **Step 4: Prove the mobile drawer is untouched in behaviour**

Narrow the window below `md` with the sidebar collapsed. Expected: the drawer opens at full width with labels, exactly as before.

---

## What is NOT covered, and what could go wrong

**The tooltip is load-bearing.** If it fails to render, the collapsed rail becomes unusable rather than merely worse. `ui/tooltip.tsx` is a shipped primitive used elsewhere, which is the mitigation, but the failure is not graceful.

**`delayDuration={0}` is a guess.** The rail is the only place in the app with tooltips on a tight vertical stack, and a delay that feels right on a button feels slow on a nav. If it flickers while moving down the rail, raise it — the number is not load-bearing and no test asserts it.

**The Playwright tests need the full acceptance-browser suite**, which builds an image and runs a container. There is no faster loop for UI behaviour in this repo, so the manual checks in Tasks 2 and 3 are how the feature is actually developed and the e2e tests are what stop it regressing.

**Nothing tests the `localStorage`-throws path.** Playwright cannot easily simulate Safari private mode, and the guard is three lines copied from a file that already documents the failure. Asserting it would need a mock this repo has no framework for.

**`aria-expanded` on `<nav>` is unusual.** It is conventional on the control, which the chevron also has. It is on the landmark too because the collapsed rail is a different presentation of the same navigation, and a screen-reader user arriving at the landmark benefits from knowing which. If it reads oddly in testing, drop it from the `<nav>` and keep it on the button.
