/* Marks the section the reader is in, in every copy of an in-page index (the
 * desktop rail and the phone strip both hold the same links). The links are
 * real `href="#id"` anchors, so the index works without this; this only adds
 * `aria-current="true"` to the link of the section under the sticky nav. */
import { stageFor } from "./home/journey";

/* Used when the page gives no scroll margin to read (an engine or a test
 * document with no stylesheet), in CSS pixels. */
const FALLBACK_OFFSET = 120;
/* Where a jump lands a section is its scroll-margin-top; this much more still counts as arrived. */
const SLACK = 8;

export function initPageIndex(root: HTMLElement): () => void {
  const win = root.ownerDocument.defaultView;
  if (!win) return () => {};

  /* A link whose fragment is not valid percent-encoding (`#%`) points at nothing: ignored. */
  const targets = new Map<HTMLAnchorElement, string>();
  for (const a of root.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')) {
    try {
      targets.set(a, decodeURIComponent(a.getAttribute("href")!.slice(1)));
    } catch {
      /* ignored */
    }
  }
  // The links must be in document order: the index is built from the page's own headings, so they are.
  const ids = [...new Set(targets.values())];
  const sections = ids
    .map((id) => root.ownerDocument.getElementById(id))
    .filter((el): el is HTMLElement => el !== null);

  /* The line a section must pass to be current, read from the page (the stylesheet
   * puts a jump's target at `scroll-margin-top`, which grows on a phone where the
   * index strip sticks under the nav), as journey.ts does. Read again on every update. */
  const lineOffset = () => {
    const m = Number.parseFloat(win.getComputedStyle(sections[0]).scrollMarginTop);
    return Number.isFinite(m) && m > 0 ? m + SLACK : FALLBACK_OFFSET; // 0 or NaN: no margin to read
  };

  let shown: string | undefined;
  const update = () => {
    if (!sections.length) return;
    const tops = sections.map((el) => el.getBoundingClientRect().top + win.scrollY);
    const de = root.ownerDocument.documentElement;
    // A short last section can never reach the line: at the very bottom it is the current one.
    const atBottom = win.scrollY + win.innerHeight >= de.scrollHeight - 2 && de.scrollHeight > 0;
    const here = sections[atBottom ? sections.length - 1 : stageFor(tops, win.scrollY, lineOffset())].id;
    if (here === shown) return;
    shown = here;
    for (const [a, id] of targets) {
      if (id === here) a.setAttribute("aria-current", "true");
      else a.removeAttribute("aria-current");
    }
  };

  let pending = false;
  let frame = 0;
  const onScroll = () => {
    if (pending) return;
    pending = true;
    frame = win.requestAnimationFrame(() => {
      pending = false;
      update();
    });
  };

  update();
  win.addEventListener("scroll", onScroll, { passive: true });
  win.addEventListener("resize", onScroll);
  return () => {
    win.removeEventListener("scroll", onScroll);
    win.removeEventListener("resize", onScroll);
    if (pending) win.cancelAnimationFrame(frame);
  };
}
