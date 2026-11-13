/* Client entry for every inner page: the homepage's boot (motion gate, Pause
 * button), then what the inner pages add. Modules are imported only when their
 * root is on the page. The page's own script calls this once the DOM is ready;
 * a failing module is logged and never stops the rest. */
import { boot } from "./home/boot";
import type { MotionGate } from "./home/motion";

export function bootInner(doc: Document): MotionGate | undefined {
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
  return gate;
}
