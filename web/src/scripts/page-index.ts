/* Marks the section the reader is in, in every copy of an in-page index (the
 * desktop rail and the phone strip both hold the same links). The links are
 * real `href="#id"` anchors, so the index works without this; this only adds
 * `aria-current="true"` to the link of the section under the sticky nav. */
import { stageFor } from "./home/journey";

/* Height of what sits over the top of the page (the nav), in CSS pixels. */
const OFFSET = 120;

export function initPageIndex(root: HTMLElement): () => void {
  const win = root.ownerDocument.defaultView;
  if (!win) return () => {};

  const links = [...root.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')];
  const ids = [...new Set(links.map((a) => decodeURIComponent(a.getAttribute("href")!.slice(1))))];
  const sections = ids
    .map((id) => root.ownerDocument.getElementById(id))
    .filter((el): el is HTMLElement => el !== null);

  const update = () => {
    if (!sections.length) return;
    const tops = sections.map((el) => el.getBoundingClientRect().top + win.scrollY);
    const here = sections[stageFor(tops, win.scrollY, OFFSET)].id;
    for (const a of links) {
      if (decodeURIComponent(a.getAttribute("href")!.slice(1)) === here) a.setAttribute("aria-current", "true");
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
