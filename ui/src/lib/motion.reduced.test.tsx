// @vitest-environment jsdom

/* THE REDUCED-MOTION PATH, WHICH IS THE ONE NOBODY EXERCISES BY HAND.
 *
 * index.css already collapses the CSS half of this system: three tokens to 0ms
 * and a blanket `transition-duration: 0.01ms !important`. None of that reaches
 * an animation driven from JavaScript -- motion writes inline styles frame by
 * frame rather than declaring a transition -- so the JS half needs its own
 * answer, and lib/motion.ts is it.
 *
 * Two things have to hold, and they pull in opposite directions:
 *
 *   1. Nothing moves. The operator asked for that.
 *   2. Everything still ARRIVES and still LEAVES. An exit animation of length
 *      zero must still run its lifecycle, because AnimatePresence unmounts the
 *      element when the exit finishes and not before. Switch the animation off
 *      instead of shortening it and the dismissed dialog stays on screen
 *      forever -- for exactly the operators who asked for less movement.
 *
 * This project has shipped the second failure once already, on the website,
 * where a reduced-motion branch removed a scroll animation and left the content
 * it was revealing pinned invisible. The last test below is that bug, in this
 * codebase, pinned in the shape it would take here.
 *
 * The other thing pinned here is jsdom. `window.matchMedia` is NOT implemented
 * by jsdom, so a hook that reads it unguarded throws on import in every one of
 * this app's 116 component test files. That is why lib/motion.ts hand-rolls the
 * media query instead of using motion's own useReducedMotion. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AcrossTheRoom } from "@/components/AcrossTheRoom";
import { MOTION, usePrefersReducedMotion, useMotionTransition } from "./motion";
import type { Status } from "@/lib/types";

afterEach(() => {
  cleanup();
  // Back to jsdom's default, which is no matchMedia at all.
  Reflect.deleteProperty(window, "matchMedia");
});

/** Installs a matchMedia that answers `matches` for the reduced-motion query.
 *
 *  Written out rather than reached for from a helper package: the shape of the
 *  object is part of what is being asserted. lib/motion.ts calls addEventListener
 *  and removeEventListener on the result, and a stub missing either one would
 *  throw in a way that only shows up in a browser. */
function stubMatchMedia(matches: boolean) {
  const listeners = new Set<(e: MediaQueryListEvent) => void>();
  const mql = {
    matches,
    media: "(prefers-reduced-motion: reduce)",
    addEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) => void listeners.add(fn),
    removeEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) =>
      void listeners.delete(fn),
  };
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn(() => mql),
  });
  return {
    /** Fires a system-level preference change mid-session. */
    change(next: boolean) {
      mql.matches = next;
      act(() => {
        for (const fn of listeners) fn({ matches: next } as MediaQueryListEvent);
      });
    },
  };
}

/** Renders a hook and reports what it returned, without a testing-library
 *  renderHook dependency this project does not have. */
function readHook<T>(use: () => T): { current: T } {
  const box = { current: undefined as T };
  function Probe() {
    box.current = use();
    return null;
  }
  render(<Probe />);
  return box;
}

describe("the reduced-motion preference", () => {
  it("does not throw where matchMedia is absent, and answers no", () => {
    // jsdom, and also any server-side render. An unimplemented browser API is
    // an answer -- "this environment has no preference" -- not an error. Every
    // component test in this tree depends on it being read this way.
    expect(typeof window.matchMedia).toBe("undefined");
    expect(readHook(usePrefersReducedMotion).current).toBe(false);
  });

  it("reports the preference when the system has one", () => {
    stubMatchMedia(true);
    expect(readHook(usePrefersReducedMotion).current).toBe(true);
  });

  it("follows a change made mid-session", () => {
    // A console stays open for a whole broadcast. Reading the preference once
    // at mount would mean the setting takes effect on the next page load, which
    // for this application is the next restart.
    const mq = stubMatchMedia(false);
    const seen = readHook(usePrefersReducedMotion);
    expect(seen.current).toBe(false);
    mq.change(true);
    expect(seen.current).toBe(true);
  });
});

describe("the transition a component gets", () => {
  it("is the token's own duration when no preference is set", () => {
    const t = readHook(() => useMotionTransition("settle"));
    expect(t.current.duration).toBe(MOTION.settle);
  });

  it("collapses to zero rather than switching off", () => {
    // ZERO, not `false` and not undefined. The distinction is the whole design:
    // a zero-length animation still starts, still finishes, and still tells
    // AnimatePresence it may unmount the element.
    stubMatchMedia(true);
    const t = readHook(() => useMotionTransition("settle"));
    expect(t.current.duration).toBe(0);
    expect(t.current.duration, "an exit that never runs never unmounts").not.toBe(false);
  });
});

describe("a dialog dismissed with reduced motion on", () => {
  it("still leaves the screen", () => {
    /* THE REGRESSION THIS FILE EXISTS FOR.
     *
     * Across-the-room mode is fixed to the viewport and paints the whole thing
     * near-black. If its exit animation does not complete, the console is
     * behind an opaque panel that Escape has already stopped answering -- the
     * operator's only way out is a page reload, mid-broadcast.
     *
     * Asserted through a real component rather than through useMotionTransition,
     * because the failure is in the interaction between a zero-length exit and
     * AnimatePresence's unmount, and a unit test of the number alone would go
     * on passing through it. */
    stubMatchMedia(true);
    const status = { ingest: null, destinations: [] } as unknown as Status;
    const { rerender } = render(
      <AcrossTheRoom open onClose={() => {}} status={status} />,
    );
    expect(screen.getByRole("dialog")).toBeTruthy();

    rerender(<AcrossTheRoom open={false} onClose={() => {}} status={status} />);
    return waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("still leaves the screen when motion is NOT reduced", () => {
    // The control. Without it the test above would pass on a component that
    // never animates at all, which is not what is being claimed.
    const status = { ingest: null, destinations: [] } as unknown as Status;
    const onClose = vi.fn();
    const { rerender } = render(<AcrossTheRoom open onClose={onClose} status={status} />);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledTimes(1);

    rerender(<AcrossTheRoom open={false} onClose={onClose} status={status} />);
    return waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
