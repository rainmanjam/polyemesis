/* Homepage boot: creates the one shared motion gate, finds each beat's root and
 * hands it to that beat's init. Each init*(root, gate) calls whileVisible
 * itself, so this file only locates roots. */
import { initFailover } from "./failover";
import { initHero } from "./hero";
import { initHowSteps } from "./how-steps";
import { createMotionGate, type MotionGate } from "./motion";
import { initUploadMeter } from "./upload-meter";

export function boot(doc: Document): MotionGate | undefined {
  const win = doc.defaultView;
  if (!win) return undefined;
  const gate = createMotionGate(win);

  for (const root of doc.querySelectorAll<HTMLElement>("[data-hero]")) {
    initHero(root, gate);
  }
  // Pause is wired here and only here (initHero un-hides it): one listener per
  // button, so one click is one setUserPaused call.
  for (const btn of doc.querySelectorAll<HTMLElement>("[data-pause]")) {
    btn.addEventListener("click", () => {
      const paused = !gate.userPaused();
      gate.setUserPaused(paused);
      btn.setAttribute("aria-pressed", String(paused));
      btn.textContent = paused ? "Resume motion" : "Pause motion";
    });
  }

  for (const root of doc.querySelectorAll<HTMLElement>("[data-upload-meter]")) {
    initUploadMeter(root, gate);
  }
  for (const root of doc.querySelectorAll<HTMLElement>("section#how")) {
    initHowSteps(root, gate);
  }
  for (const root of doc.querySelectorAll<HTMLElement>("[data-failover]")) {
    initFailover(root, gate);
  }
  return gate;
}
