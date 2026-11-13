/* Client entry for every inner page: the homepage's boot (motion gate, Pause
 * button), then what the inner pages add. Modules are imported only when their
 * root is on the page. Called from the page's own script; it waits for DOMContentLoaded if the DOM is still loading;
 * a failing module is logged and never stops the rest. */
import { boot } from "./home/boot";
import type { MotionGate } from "./home/motion";

export function bootInner(doc: Document): MotionGate | undefined {
  // Called from a script that may run before the DOM is parsed: wait for it.
  if (doc.readyState === "loading") {
    doc.addEventListener("DOMContentLoaded", () => bootInner(doc), { once: true });
    return undefined;
  }
  const gate = boot(doc);

  // Controls that do nothing without script render hidden; this shows them.
  for (const el of doc.querySelectorAll<HTMLElement>("[data-js-only]")) el.hidden = false;

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
