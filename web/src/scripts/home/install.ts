/* "Install": the method tabs (Docker, Compose, script).
 *
 * The server renders all three panels, each under its own heading, with the
 * tablist hidden: without script every command is on the page and readable.
 * This marks the section ready (the stylesheet then shows the tablist and drops
 * the per-panel headings) and lets initTabs show one panel at a time. The tab
 * behaviour itself is the shared controller in ../tabs. */
import { initTabs } from "../tabs";
import type { MotionGate } from "./motion";

export function initInstall(root: HTMLElement, _gate: MotionGate): () => void {
  const tablist = root.querySelector<HTMLElement>("[data-install-tabs]");
  if (!tablist) return () => {};
  const panels = [...root.querySelectorAll<HTMLElement>('[role="tabpanel"]')];

  root.dataset.ready = "true";
  const tabs = initTabs(tablist, () => {});

  return () => {
    tabs.destroy();
    delete root.dataset.ready;
    for (const p of panels) p.hidden = false;
  };
}
