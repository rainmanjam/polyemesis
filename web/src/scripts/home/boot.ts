/* Homepage boot: creates the one shared motion gate, finds each beat's root and
 * hands it to that beat's init. Each init*(root, gate) calls whileVisible
 * itself, so this file only locates roots. */
import { initFailover } from "./failover";
import { initHowSteps } from "./how-steps";
import { createMotionGate, type MotionGate } from "./motion";
import { initUploadMeter } from "./upload-meter";

export function boot(doc: Document): MotionGate | undefined {
  const win = doc.defaultView;
  if (!win) return undefined;
  const gate = createMotionGate(win);

  // TODO(task 4): hero init and the Pause control wiring go here, before the
  // beats, so they share `gate`.

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
