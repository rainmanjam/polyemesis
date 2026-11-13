/* Homepage boot: creates the one shared motion gate, wires Pause, then starts
 * each section whose root is on the page. A section's module is imported only
 * when its root is present, so a section whose module has not landed yet is
 * never requested. One failing section (a throwing init or a rejected import)
 * is contained: it never stops the other sections or the Pause button. */
import { createMotionGate, type MotionGate } from "./motion";

type Init = (root: HTMLElement, gate: MotionGate) => unknown;

/* Root selector -> loader. Each later task adds its one line here, using an
 * explicit string-literal import path so the bundler can resolve it. */
const SECTIONS: ReadonlyArray<readonly [selector: string, load: () => Promise<Init>]> = [
  ["[data-hero]", () => import("./hero").then((m) => m.initHero)],
  ["[data-how]", () => import("./how-steps").then((m) => m.initHowSteps)],
  ["[data-matrix]", () => import("./matrix").then((m) => m.initMatrix)],
  ["[data-panels]", () => import("./panels").then((m) => m.initPanels)],
  ["[data-tour]", () => import("./tour").then((m) => m.initTour)],
  ["[data-deck]", () => import("./deck/index").then((m) => m.initDeck)],
  ["[data-calc]", () => import("./calc-mini").then((m) => m.initCalc)],
  ["[data-install]", () => import("./install").then((m) => m.initInstall)],
  ["[data-journey]", () => import("./journey").then((m) => m.initJourney)],
];

export function boot(doc: Document): MotionGate | undefined {
  const win = doc.defaultView;
  if (!win) return undefined;
  const gate = createMotionGate(win);

  // Pause is wired here and only here: one listener per button, so one click is
  // one setUserPaused call. The markup renders it hidden (without JavaScript it
  // would be a control that does nothing); wiring it is what shows it.
  for (const btn of doc.querySelectorAll<HTMLElement>("[data-pause]")) {
    btn.hidden = false;
    btn.addEventListener("click", () => {
      const paused = !gate.userPaused();
      gate.setUserPaused(paused);
      btn.setAttribute("aria-pressed", String(paused));
      btn.textContent = paused ? "Resume motion" : "Pause motion";
    });
  }

  for (const [selector, load] of SECTIONS) {
    for (const root of doc.querySelectorAll<HTMLElement>(selector)) {
      // The page never tears its sections down, so whatever an init returns
      // (a disposer or a controller object) is discarded.
      load()
        .then((init) => {
          init(root, gate);
        })
        .catch((err) => console.error(`boot: section ${selector} failed to start`, err));
    }
  }
  return gate;
}
