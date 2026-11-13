/* Client entry for every inner page. Shares the homepage's boot (motion gate,
 * Pause button); the in-page index is added by the next task. */
import { boot } from "./home/boot";

export function bootInner(doc: Document): void {
  boot(doc);
}
