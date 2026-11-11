/* Beat 3, "how it works": a CSS-sticky stage whose step follows scroll progress.
 *
 * The page never hijacks scrolling. The track is simply taller than the
 * viewport and the stage sticks inside it; this module only reads how far the
 * track has scrolled and lights the matching step. Scroll progress comes from JS
 * because CSS scroll-driven timelines are not allowed on this site. Without JS,
 * or below 768px, nothing pins and every step stays open. */

import { whileVisible, type MotionGate } from "./motion";

const STEP_COUNT = 5;
const PIN_QUERY = "(min-width: 768px)";

const clamp01 = (n: number) => Math.min(1, Math.max(0, n));

/** How far through its pinned run the stage is, 0..1. `trackTop` is the
 * track's top edge relative to the viewport; `navH` is the sticky offset. The
 * stage pins when the track's top reaches the nav's bottom (progress 0) and
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
  const dg = root.querySelector<HTMLElement>(".dg");
  const steps = [...root.querySelectorAll<HTMLElement>(".step")];
  const pips = [...root.querySelectorAll<HTMLButtonElement>("[data-goto]")];
  const parts = dg ? [...dg.querySelectorAll<HTMLElement>("[data-on]")] : [];
  if (!track) return () => {};

  const stopPlaying = dg
    ? whileVisible(
        dg,
        gate,
        () => dg.classList.add("is-playing"),
        () => dg.classList.remove("is-playing"),
      )
    : () => {};

  const mq = win.matchMedia(PIN_QUERY);
  let current = 0;
  let frame = 0;
  let enhanced = false;
  let navH = 0;

  const measureNav = () => {
    const nav = doc.querySelector("header nav");
    if (!nav) return;
    navH = Math.round(nav.getBoundingClientRect().height);
    root.style.setProperty("--nav-h", `${navH}px`);
  };

  const apply = (step: number) => {
    if (step === current) return;
    current = step;
    steps.forEach((el, i) => el.classList.toggle("on", i + 1 === step));
    pips.forEach((el, i) => {
      if (i + 1 === step) el.setAttribute("aria-current", "step");
      else el.removeAttribute("aria-current");
    });
    if (dg) {
      dg.dataset.step = String(step);
      for (const el of parts) {
        el.classList.toggle("off", !(el.dataset.on ?? "").split(/\s+/).includes(String(step)));
      }
    }
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
    measureNav();
    onScroll();
  };

  const goto = (n: number) => {
    const r = track.getBoundingClientRect();
    const range = r.height - win.innerHeight + navH;
    win.scrollTo({
      // The middle of step n's band: the inverse of progressOf.
      top: win.scrollY + r.top - navH + (range * (n - 0.5)) / STEP_COUNT,
      // "instant", not "auto": "auto" defers to the page's scroll-behavior, and
      // the CSS and the gate are two answers that could drift; "instant" is
      // instant whatever the stylesheet says.
      behavior: gate.allowed() ? "smooth" : "instant",
    });
  };
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
    measureNav();
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
    dg?.removeAttribute("data-step");
    parts.forEach((el) => el.classList.remove("off"));
  };

  const sync = () => (mq.matches ? enhance() : unenhance());
  mq.addEventListener("change", sync);
  sync();

  return () => {
    mq.removeEventListener("change", sync);
    unenhance();
    for (const [el, fn] of pipHandlers) el.removeEventListener("click", fn);
    stopPlaying();
  };
}
