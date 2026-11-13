/* "How it works": a CSS-sticky stage whose step follows scroll progress.
 *
 * The page never hijacks scrolling. The track is simply taller than the
 * viewport and the stage sticks inside it; this module only reads how far the
 * track has scrolled and lights the matching step. Scroll progress comes from JS
 * because CSS scroll-driven timelines are not allowed on this site. Without JS,
 * or below 768px, nothing pins and every step stays open. */

import { whileVisible, type MotionGate } from "./motion";
import { scrollToPosition } from "./scroll";

const STEP_COUNT = 3;
const PIN_QUERY = "(min-width: 768px)";

const clamp01 = (n: number) => Math.min(1, Math.max(0, n));

/** How far through its pinned run the stage is, 0..1. `trackTop` is the
 * track's top edge relative to the viewport; `navH` is the sticky offset: the
 * nav's height plus the journey bar's when the bar is on the page. The stage
 * pins when the track's top reaches the bottom of that stack (progress 0) and
 * lets go when the track's bottom reaches the viewport's (progress 1). */
export function progressOf(trackTop: number, trackHeight: number, viewportH: number, navH = 0): number {
  const range = trackHeight - viewportH + navH;
  if (range <= 0) return 0;
  return clamp01((navH - trackTop) / range);
}

/** The step (1..count) that a progress value falls in. */
export function stepFor(progress: number, count = STEP_COUNT): number {
  return Math.min(count, Math.floor(progress * count) + 1);
}

export function initHowSteps(root: HTMLElement, gate: MotionGate): () => void {
  const win = root.ownerDocument.defaultView!;
  const doc = root.ownerDocument;
  const track = root.querySelector<HTMLElement>(".how-track");
  const stage = root.querySelector<HTMLElement>(".how-stage");
  const steps = [...root.querySelectorAll<HTMLElement>(".step")];
  const pips = [...root.querySelectorAll<HTMLButtonElement>("[data-goto]")];
  if (!track) return () => {};

  // The diagrams' flowing dashes run only while the stage is on screen and the
  // gate allows motion; the stylesheet keys them off .is-playing.
  const stopPlaying = stage
    ? whileVisible(
        stage,
        gate,
        () => stage.classList.add("is-playing"),
        () => stage.classList.remove("is-playing"),
      )
    : () => {};

  const mq = win.matchMedia(PIN_QUERY);
  let current = 0;
  let frame = 0;
  let enhanced = false;
  let navH = 0;

  /* The height of everything stuck above the stage: the nav, plus the journey
   * bar when the page has one. Published as --stack-h on the section so the
   * stylesheet's sticky offset is the measured one, whichever of the two
   * layouts the page is in. */
  const measureStack = () => {
    const nav = doc.querySelector("header nav");
    if (!nav) return;
    const bar = doc.querySelector("[data-journey]");
    navH = Math.round(nav.getBoundingClientRect().height + (bar ? bar.getBoundingClientRect().height : 0));
    root.style.setProperty("--stack-h", `${navH}px`);
  };

  const apply = (step: number) => {
    if (step === current) return;
    current = step;
    steps.forEach((el, i) => el.classList.toggle("on", i + 1 === step));
    pips.forEach((el, i) => {
      if (i + 1 === step) el.setAttribute("aria-current", "step");
      else el.removeAttribute("aria-current");
    });
  };

  const update = () => {
    frame = 0;
    const r = track.getBoundingClientRect();
    apply(stepFor(progressOf(r.top, r.height, win.innerHeight, navH)));
  };

  const onScroll = () => {
    if (!frame) frame = win.requestAnimationFrame(update);
  };
  const onResize = () => {
    measureStack();
    onScroll();
  };

  const goto = (n: number) => {
    const r = track.getBoundingClientRect();
    const range = r.height - win.innerHeight + navH;
    scrollToPosition(
      win,
      // The middle of step n's band: the inverse of progressOf. navH is the
      // whole stack, so a step never lands under the journey bar.
      { top: win.scrollY + r.top - navH + (range * (n - 0.5)) / STEP_COUNT },
      // "instant" (see scroll.ts), not "auto": "auto" defers to the stylesheet,
      // and the CSS and the gate are two answers that could drift.
      gate.allowed(),
    );
  };
  /* A step that is not lit has its text clipped to nothing; a link inside it can
   * still take keyboard focus. Bring that step forward, so what has focus is
   * also what is shown (the stylesheet's :focus-within rule covers the instant
   * before this runs, and the page without the pinned layout never clips). */
  const onFocusIn = (e: FocusEvent) => {
    if (!enhanced) return;
    const step = (e.target as Element | null)?.closest<HTMLElement>(".step");
    const n = Number(step?.dataset.step);
    if (Number.isInteger(n) && n >= 1 && n <= STEP_COUNT && n !== current) goto(n);
  };
  root.addEventListener("focusin", onFocusIn);
  const pipHandlers = pips.map((el) => {
    const n = Number(el.dataset.goto);
    const fn = () => goto(n);
    el.addEventListener("click", fn);
    return [el, fn] as const;
  });

  const enhance = () => {
    if (enhanced) return;
    enhanced = true;
    root.classList.add("is-enhanced");
    measureStack();
    win.addEventListener("scroll", onScroll, { passive: true });
    win.addEventListener("resize", onResize);
    update();
  };
  const unenhance = () => {
    if (!enhanced) return;
    enhanced = false;
    root.classList.remove("is-enhanced");
    win.removeEventListener("scroll", onScroll);
    win.removeEventListener("resize", onResize);
    if (frame) win.cancelAnimationFrame(frame);
    frame = 0;
    current = 0;
    steps.forEach((el) => el.classList.remove("on"));
    pips.forEach((el) => el.removeAttribute("aria-current"));
    root.style.removeProperty("--stack-h");
  };

  const sync = () => (mq.matches ? enhance() : unenhance());
  mq.addEventListener("change", sync);
  sync();

  return () => {
    mq.removeEventListener("change", sync);
    unenhance();
    for (const [el, fn] of pipHandlers) el.removeEventListener("click", fn);
    root.removeEventListener("focusin", onFocusIn);
    stopPlaying();
  };
}
