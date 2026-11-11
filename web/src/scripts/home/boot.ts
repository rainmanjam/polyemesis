/* Finds each homepage beat's root and hands it to that beat's init. Each init
 * wires its own whileVisible, so this file only locates roots and shares the
 * one motion gate. */
import { createMotionGate } from "./motion";
import { initUploadMeter } from "./upload-meter";

export function boot(doc: Document): void {
  const win = doc.defaultView;
  if (!win) return;
  const gate = createMotionGate(win);
  for (const root of doc.querySelectorAll<HTMLElement>("[data-upload-meter]")) {
    initUploadMeter(root, gate);
  }
}
