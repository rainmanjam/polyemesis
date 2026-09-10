import { useEffect, useState } from "react";
import type { Transition } from "motion/react";

/* ===========================================================================
   THE MOTION VOCABULARY, IN JAVASCRIPT.

   index.css is the source of truth for this system — see the `:root` block
   there and docs/DESIGN-SYSTEM.md. Colour, type and motion are all declared
   once, in plain CSS, so the website can include the same block.

   CSS cannot animate an element that is being unmounted. That is the entire
   reason this app now carries the `motion` package: a dialog that vanishes the
   instant React drops it never tells the operator it closed, and no amount of
   `transition:` fixes that because by the time the transition would run the
   node is gone. Every animation written against this module is an ENTER/EXIT
   pair; anything that can be expressed as a CSS `transition` still is, and
   `--default-transition-duration` in the `@theme inline` block already points
   Tailwind's whole transition system at these same tokens.

   WHY THE NUMBERS ARE RESTATED HERE. A JS animation cannot read a CSS custom
   property without a live element and a getComputedStyle call — which returns
   nothing in jsdom, so every unit test would silently animate at whatever the
   fallback was. Restating them is honest and the drift is caught by
   motion.test.ts, which parses index.css and fails if a token here stops
   matching the token there. That is the same shape as preset-drift.test.ts and
   tour-drift.test.ts elsewhere in this tree: duplicate the value, then make
   duplicating it wrong a test failure rather than a code review.

   SECONDS, NOT MILLISECONDS. motion takes seconds; CSS takes milliseconds. The
   conversion happens once, here, rather than at each call site where a missing
   `/ 1000` is a 90-second fade nobody notices in review.

   THE PACKAGE IS THE WEBSITE'S. web/package.json already carries `motion` v13
   (web/src/components/MixMatrix.astro animates the crosspoints with it), so
   this is the same dependency on both sides of the product rather than a second
   animation library with its own easing conventions. Same reasoning as the
   SPINE comments in index.css: one product, one set of facts.

   WHAT IT COSTS, measured rather than estimated. `npx vite build` before and
   after, on the main chunk:

       before                 2,702.50 kB   gzip 835.35 kB
       motion/react           2,828.68 kB   gzip 876.95 kB   (+41.60 kB gzip)
       LazyMotion + m.*       2,782.18 kB   gzip 863.33 kB   (+27.98 kB gzip)

   The LazyMotion route -- `m.div` plus a <LazyMotion features={domAnimation}>
   provider -- was measured and rejected, not overlooked. It saves 13.6 kB
   gzipped and buys a footgun: `m.div` rendered outside the provider mounts
   perfectly and simply never animates, with no error anywhere, which is the
   exact class of silent failure the rest of this codebase writes drift tests to
   avoid. 41.6 kB on a bundle that already ships 835 kB, served from a Go binary
   on the operator's own LAN and cached after the first load, is not worth
   trading for a component that can be wired up wrong invisibly.

   THE SITE USES motion/mini AND THIS DOES NOT, for a reason: `motion/mini` is
   2.5 kB and animates DOM nodes that exist. It has no AnimatePresence, so it
   cannot animate a React unmount, which is the entire thing CSS could not do
   and the entire reason this dependency is here.
   =========================================================================== */

/** The three durations from docs/DESIGN-SYSTEM.md, in seconds.
 *
 *  A console animates to EXPLAIN a state change, never to decorate one, and
 *  which tier a thing gets is a statement about what kind of change it is:
 *
 *  - `instant` (90ms)  hover, focus ring, button press
 *  - `quick`   (160ms) disclosure, popover, tab change
 *  - `settle`  (260ms) dialog, drawer, page transition
 *
 *  There is no fourth. A fourth gets chosen by accident, and then the tiers
 *  stop meaning anything. */
export const MOTION = {
  instant: 0.09,
  quick: 0.16,
  settle: 0.26,
} as const;

export type MotionStep = keyof typeof MOTION;

/** `--ease-out`, as motion's cubic-bezier control points.
 *
 *  cubic-bezier(0.2, 0, 0, 1): almost all of the movement happens in the first
 *  third. That is what makes a 260ms dialog feel immediate rather than slow —
 *  the operator sees the answer well before the animation finishes. */
export const EASE_OUT = [0.2, 0, 0, 1] as const;

/** Whether the operator's system has asked for less motion.
 *
 *  Not motion's own `useReducedMotion`: that reads `window.matchMedia`
 *  unguarded, and jsdom does not implement matchMedia at all, so every one of
 *  this app's component tests would throw on import. The guard below is the
 *  same one lib/i18n.ts uses around localStorage — a browser API that is
 *  absent is not an error, it is an answer.
 *
 *  Subscribed rather than read once: an operator who turns the system setting
 *  on mid-session has asked this application too, and a console stays open for
 *  a whole broadcast. */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => query()?.matches ?? false);

  useEffect(() => {
    const mq = query();
    if (!mq) return;
    const onChange = (e: MediaQueryListEvent) => setReduced(e.matches);
    mq.addEventListener("change", onChange);
    // No re-read on subscribe. The initial value above is read during the same
    // render that schedules this effect, so the only change it could miss is
    // one made inside that gap -- and a setState here to close it is a second
    // render on every mount of every component that asks about motion, which
    // the linter objects to for exactly that reason.
    return () => mq.removeEventListener("change", onChange);
  }, []);

  return reduced;
}

function query(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  return window.matchMedia("(prefers-reduced-motion: reduce)");
}

/** The transition for one tier, honouring the reduced-motion preference.
 *
 *  NAMED `useMotionTransition`, NOT `useTransition`, because React 19 exports a
 *  hook of that name and it does something completely different — it marks a
 *  state update as non-urgent. Two identically named hooks in one app is one
 *  editor auto-import away from a component that compiles, renders, and quietly
 *  does the wrong thing. The prefix costs six characters.
 *
 *  COLLAPSED TO ZERO, NOT SWITCHED OFF, which is the same choice index.css
 *  makes for the CSS half of this system. A zero-duration animation still runs
 *  its lifecycle: `animate` still lands on its target values and — the part
 *  that matters — an `exit` still completes, so AnimatePresence still unmounts
 *  the element. Returning `false` or dropping the animation instead would leave
 *  a dismissed dialog on screen forever for exactly the operators who asked for
 *  less movement.
 *
 *  That failure has already been shipped once on the website, where a
 *  reduced-motion path removed a scroll animation and left the content it was
 *  revealing pinned invisible. Suppressing motion must never suppress what the
 *  motion was saying. */
export function useMotionTransition(step: MotionStep): Transition {
  const reduced = usePrefersReducedMotion();
  return {
    duration: reduced ? 0 : MOTION[step],
    ease: EASE_OUT,
  };
}
