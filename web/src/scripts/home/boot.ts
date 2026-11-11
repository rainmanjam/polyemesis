/* Homepage boot: finds each beat's root and hands it to its initialiser.
 * Each init*(root, gate) calls whileVisible itself, so this file only finds
 * roots. Task 4 owns the rest of this file (Pause wiring, hero, the other
 * beats); this version carries the recovery beat only. */

import { initFailover } from "./failover";
import { createMotionGate, type MotionGate } from "./motion";

export function boot(doc: Document): MotionGate {
  const gate = createMotionGate(doc.defaultView ?? window);
  const failover = doc.querySelector<HTMLElement>("[data-failover]");
  if (failover) initFailover(failover, gate);
  return gate;
}
