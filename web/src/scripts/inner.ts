/* Client entry for every inner page: the homepage's boot (motion gate, Pause
 * button), then what the inner pages add. Modules are imported only when their
 * root is on the page. Called from the page's own script; it waits for DOMContentLoaded if the DOM is still loading;
 * a failing module is logged and never stops the rest. */
import { boot } from "./home/boot";
import type { MotionGate } from "./home/motion";

/** A page opened at `#section` is scrolled there before this script runs. The
 * script then shows the controls it drives and hides what they replace (the
 * install tabs collapse four panels to one), which moves the section away from
 * where the browser put it. Put it back, at once, on a fresh navigation only:
 * a reload or a Back keeps the position the visitor left. Modules that load
 * later move the page again, so the section is held in place while the page
 * settles, for one second at most, and let go the moment the visitor scrolls,
 * taps or types, or the page moves by anything but this code (a scrollbar drag,
 * find-in-page, an assistive technology). A section inside a tab panel that the
 * tabs module has hidden gets its tab opened first. */
const SETTLE_MS = 1000;
const LET_GO = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
/* A scroll this far from where align() left the page is not the page's own. */
const OWN_SCROLL_PX = 1;

/** Opens the tab of the hidden panel that holds `target`, if there is one. */
function openPanelOf(doc: Document, target: HTMLElement): void {
  const panel = target.closest<HTMLElement>("[role=tabpanel]");
  if (!panel?.hidden) return;
  const tab = [...doc.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.getAttribute("aria-controls") === panel.id);
  tab?.click();
}

function realignFragment(doc: Document): void {
  const win = doc.defaultView;
  const hash = win?.location.hash ?? "";
  if (!win || hash.length < 2) return;
  const nav = win.performance?.getEntriesByType?.("navigation")[0] as PerformanceNavigationTiming | undefined;
  if (nav && nav.type !== "navigate") return;
  let id: string;
  try {
    id = decodeURIComponent(hash.slice(1));
  } catch {
    return;
  }
  const target = doc.getElementById(id);
  if (typeof target?.scrollIntoView !== "function") return;
  let held = true;
  let left = 0; // where align() last left the page
  const align = () => {
    if (!held) return;
    openPanelOf(doc, target);
    target.scrollIntoView({ block: "start", behavior: "instant" });
    left = win.scrollY;
  };
  align();
  if (typeof win.ResizeObserver !== "function") return;
  const watch = new win.ResizeObserver(align);
  watch.observe(doc.body);
  const stop = () => {
    held = false;
    watch.disconnect();
    for (const type of LET_GO) win.removeEventListener(type, stop);
    win.removeEventListener("scroll", onScroll);
  };
  const onScroll = () => {
    if (Math.abs(win.scrollY - left) > OWN_SCROLL_PX) stop();
  };
  for (const type of LET_GO) win.addEventListener(type, stop, { passive: true });
  win.addEventListener("scroll", onScroll, { passive: true });
  win.setTimeout(stop, SETTLE_MS);
}

export function bootInner(doc: Document): MotionGate | undefined {
  // Called from a script that may run before the DOM is parsed: wait for it.
  if (doc.readyState === "loading") {
    doc.addEventListener("DOMContentLoaded", () => bootInner(doc), { once: true });
    return undefined;
  }
  const gate = boot(doc);

  // Controls that do nothing without script render hidden; this shows them.
  for (const el of doc.querySelectorAll<HTMLElement>("[data-js-only]")) el.hidden = false;
  realignFragment(doc);

  const indexes = [...doc.querySelectorAll<HTMLElement>("[data-index]")];
  if (indexes.length) {
    import("./page-index")
      .then((m) => {
        for (const root of indexes) m.initPageIndex(root);
      })
      .catch((err) => console.error("inner: the page index failed to start", err));
  }

  const choosers = [...doc.querySelectorAll<HTMLElement>("[data-chooser]")];
  if (choosers.length) {
    import("./chooser")
      .then((m) => {
        for (const root of choosers) m.initChooser(root);
      })
      .catch((err) => console.error("inner: the product chooser failed to start", err));
  }

  const failovers = [...doc.querySelectorAll<HTMLElement>("[data-failover]")];
  if (failovers.length && gate) {
    import("./home/failover")
      .then((m) => {
        for (const el of failovers) m.initFailover(el, gate);
      })
      .catch((err) => console.error("inner: the failover demo failed to start", err));
  }
  return gate;
}
