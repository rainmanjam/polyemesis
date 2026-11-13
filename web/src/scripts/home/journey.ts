/* The journey bar: marks which stage of the homepage (Learn, Understand, Why,
 * Install, Use) the visitor is in, and jumps between stages.
 *
 * Sections declare their stage with data-journey-stage (not data-stage, which
 * the failover panel uses for its own buttons). The bar's links name theirs
 * with data-journey-link. Without any stage element there is nothing to mark
 * and the links stay ordinary anchors. */
import type { MotionGate } from "./motion";
import { scrollToPosition } from "./scroll";

/** Index of the last stage whose top is at or above `scrollY + offset`; 0
 * before the first. `offset` is the height of whatever is stuck over the top
 * of the page (the nav and this bar), which hides the first pixels of a
 * section that has, in effect, already arrived. */
export function stageFor(tops: number[], scrollY: number, offset: number): number {
  const line = scrollY + offset;
  let found = 0;
  for (let i = 0; i < tops.length; i++) {
    if (tops[i] <= line) found = i;
  }
  return found;
}

export function initJourney(root: HTMLElement, gate: MotionGate): () => void {
  const doc = root.ownerDocument;
  const win = doc.defaultView;
  if (!win) return () => {};

  const links = [...root.querySelectorAll<HTMLAnchorElement>("a[data-journey-link]")];

  /* First section of each stage, in document order. */
  const firstOfStage = new Map<string, HTMLElement>();
  for (const el of doc.querySelectorAll<HTMLElement>("[data-journey-stage]")) {
    const name = el.dataset.journeyStage ?? "";
    if (name && !firstOfStage.has(name)) firstOfStage.set(name, el);
  }
  const stages = [...firstOfStage].map(([name, el]) => ({
    el,
    link: links.find((a) => a.dataset.journeyLink === name),
  }));

  /* Height of what is stuck over the page: the nav and this bar. */
  const offset = () => {
    const nav = doc.querySelector("header nav");
    return Math.round((nav?.getBoundingClientRect().height ?? 0) + root.getBoundingClientRect().height);
  };
  /* Where a click lands the section: below the stuck nav and bar by the same gap
   * the stylesheet gives an anchor (scroll-margin-top). Read from the target so
   * the two cannot disagree; the fallback is for an engine that reports none. */
  /* --anchor-gap in global.css is the source; 16 is only for an engine (or a test
   * document) with no stylesheet to read it from. */
  const gap = () => {
    const v = Number.parseFloat(win.getComputedStyle(doc.documentElement).getPropertyValue("--anchor-gap"));
    return Number.isFinite(v) ? v : 16;
  };
  const landing = (el: HTMLElement) => {
    const m = Number.parseFloat(win.getComputedStyle(el).scrollMarginTop);
    return Number.isFinite(m) && m > 0 ? m : offset() + gap();
  };
  const topOf = (el: HTMLElement) => win.scrollY + el.getBoundingClientRect().top;

  let frame = 0;
  let shown = -1; // the stage whose link was last brought into view
  const update = () => {
    frame = 0;
    if (stages.length === 0) return;
    const at = stageFor(
      stages.map((s) => topOf(s.el)),
      win.scrollY,
      // A section a click has landed is one gap below the bar, and counts as arrived.
      offset() + gap(),
    );
    stages.forEach((s, i) => {
      if (!s.link) return;
      if (i === at) s.link.setAttribute("aria-current", "true");
      else s.link.removeAttribute("aria-current");
    });
    // The bar scrolls sideways where its five items do not fit (a 320px phone): when the stage changes,
    // bring its link fully into view. Smooth only while motion is allowed.
    if (at !== shown) {
      shown = at;
      const link = stages[at]?.link;
      const list = link?.closest("ol");
      if (link && list) {
        const box = list.getBoundingClientRect();
        const r = link.getBoundingClientRect();
        const dx = r.left < box.left ? r.left - box.left : r.right > box.right ? r.right - box.right : 0;
        if (dx) scrollToPosition(list, { left: list.scrollLeft + dx }, gate.allowed());
      }
    }
  };
  const schedule = () => {
    if (!frame) frame = win.requestAnimationFrame(update);
  };

  const onClick = (e: MouseEvent) => {
    // Open in a new tab / window, download and the like belong to the browser.
    if (e.defaultPrevented || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    const a = (e.target as Element | null)?.closest<HTMLAnchorElement>("a[href^='#']");
    if (!a || !root.contains(a)) return;
    const id = a.getAttribute("href")!.slice(1);
    const target = doc.getElementById(id);
    if (!target) return; // nothing to jump to: leave the anchor alone
    e.preventDefault();
    scrollToPosition(
      win,
      {
        // Rounded UP: a browser settles on a whole pixel, and a landing a fraction
        // short leaves the section's top just below the line, so the stage before
        // it would stay marked.
        top: Math.ceil(topOf(target) - landing(target)),
      },
      gate.allowed(),
    );
    /* What the browser would have done with the anchor, minus its own jump:
     * focus moves to the section (so the next Tab starts there) and the address
     * names it (so Back, refresh and sharing land on it). */
    if (!target.hasAttribute("tabindex")) target.setAttribute("tabindex", "-1");
    target.focus({ preventScroll: true });
    try {
      win.history.replaceState(null, "", `#${id}`);
    } catch {
      // a sandboxed or opaque origin refuses; the jump has still happened
    }
  };

  win.addEventListener("scroll", schedule, { passive: true });
  win.addEventListener("resize", schedule);
  root.addEventListener("click", onClick);
  update();

  return () => {
    win.removeEventListener("scroll", schedule);
    win.removeEventListener("resize", schedule);
    root.removeEventListener("click", onClick);
    if (frame) win.cancelAnimationFrame(frame);
    frame = 0;
  };
}
