/* The homepage motion gate: one answer to "may anything move right now?".
 *
 * Motion is allowed only when the visitor has not asked the OS for reduced
 * motion AND has not pressed the page's Pause control. The answer is mirrored
 * onto <html class="motion-on"> so CSS can key off it, and pushed to subscribers
 * so scripted animation can stop in the same tick. */

export interface MotionGate {
  allowed(): boolean;
  userPaused(): boolean;
  setUserPaused(p: boolean): void;
  subscribe(fn: (allowed: boolean) => void): () => void;
}

const REDUCED = "(prefers-reduced-motion: reduce)";

/** The gate plus a disposer for its media-query listener (the page keeps one
 * gate for its lifetime; tests and any future teardown use dispose). */
export type OwnedMotionGate = MotionGate & { dispose(): void };

export function createMotionGate(win: Window = window): OwnedMotionGate {
  const mq = win.matchMedia(REDUCED);
  const root = win.document.documentElement;
  const subs = new Set<(allowed: boolean) => void>();
  let paused = false;
  let last: boolean;

  const allowed = () => !mq.matches && !paused;

  const sync = () => {
    const now = allowed();
    root.classList.toggle("motion-on", now);
    if (now === last) return;
    last = now;
    for (const fn of [...subs]) {
      /* One failing subscriber must not leave the rest moving (or stopped).
       * The error is still reported, so it is never silent. */
      try {
        fn(now);
      } catch (err) {
        if (typeof win.reportError === "function") win.reportError(err);
        else setTimeout(() => {
          throw err;
        });
      }
    }
  };

  last = allowed();
  root.classList.toggle("motion-on", last);
  mq.addEventListener("change", sync);

  return {
    allowed,
    userPaused: () => paused,
    setUserPaused(p) {
      paused = p;
      sync();
    },
    subscribe(fn) {
      subs.add(fn);
      return () => {
        subs.delete(fn);
      };
    },
    dispose() {
      mq.removeEventListener("change", sync);
      subs.clear();
    },
  };
}

/* Runs `start` only while the element is on screen, the gate allows motion and
 * the tab is visible; `stop` otherwise. Calls are edge-triggered, so start and
 * stop strictly alternate and `stop` is never called before the first `start`.
 * Returns a disposer that stops (if running) and detaches every listener. */
export function whileVisible(
  el: Element,
  gate: MotionGate,
  start: () => void,
  stop: () => void,
  threshold = 0.1,
): () => void {
  const doc = el.ownerDocument;
  let intersecting = false;
  let running = false;

  const update = () => {
    const should = intersecting && gate.allowed() && doc.visibilityState === "visible";
    if (should === running) return;
    running = should;
    if (should) start();
    else stop();
  };

  const io = new IntersectionObserver(
    (entries) => {
      const last = entries[entries.length - 1];
      if (last) intersecting = last.isIntersecting;
      update();
    },
    { threshold },
  );
  io.observe(el);
  const unsub = gate.subscribe(update);
  doc.addEventListener("visibilitychange", update);

  return () => {
    io.disconnect();
    unsub();
    doc.removeEventListener("visibilitychange", update);
    intersecting = false;
    update();
  };
}
