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
 * settles, for two seconds at most, and let go the moment the visitor scrolls,
 * taps or types. */
const SETTLE_MS = 2000;
const LET_GO = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

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
  const align = () => target.scrollIntoView({ block: "start", behavior: "instant" });
  align();
  if (typeof win.ResizeObserver !== "function") return;
  const watch = new win.ResizeObserver(align);
  watch.observe(doc.body);
  const stop = () => {
    watch.disconnect();
    for (const type of LET_GO) win.removeEventListener(type, stop);
  };
  for (const type of LET_GO) win.addEventListener(type, stop, { passive: true });
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

  const chooser = doc.querySelector<HTMLElement>("[data-chooser]");
  if (chooser) {
    import("./chooser")
      .then((m) => m.initChooser(chooser))
      .catch((err) => console.error("inner: the product chooser failed to start", err));
  }
  return gate;
}
