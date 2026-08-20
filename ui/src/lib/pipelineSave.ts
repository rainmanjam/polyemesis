import type { Settings } from "./types";

/* What a Save on the Pipeline settings tab actually commits.
 *
 * TWO defects, one root. The tab held ONE draft and offered NINE Save buttons
 * over it, eight of which called the plain settings save:
 *
 *   - The MQTT broker password does not live in the settings blob. It is a
 *     separate PUT, made only by the MQTT card's own button. Type a password,
 *     click any of the other eight -- "Settings saved.", the password never
 *     sent, and the effect that re-seeds the draft from the server's reply
 *     wiped the box. `hasPassword` still false, under a success toast.
 *   - Every one of those buttons PUT the whole tab draft anyway, so saving the
 *     failover slate also committed an abandoned chat-retention edit. The
 *     buttons were never per-card; only their placement suggested they were.
 *
 * The fix is a CONTROL, not a warning beside eight buttons. The tab has one
 * Save, it commits the whole draft because that is what it always did, and it
 * carries the typed password when there is one. There is no longer an
 * expression in which a control on this tab discards it. */

/** Whether this Save must also PUT the broker password.
 *
 *  Empty means "leave the stored one alone" -- the box is never seeded from
 *  the server, because the server does not send the password back -- so an
 *  untouched card takes the plain settings path and does not disturb what is
 *  stored. */
export function pipelineSaveCarriesPassword(mqttPassword: string): boolean {
  return mqttPassword !== "";
}

/** Whether there is anything on the tab worth saving.
 *
 *  Includes the password box: a typed password with an otherwise untouched
 *  draft is still an unsaved change, and was the exact case that used to be
 *  thrown away in silence. */
export function pipelineDirty(
  server: Settings,
  draft: Settings,
  mqttPassword: string,
): boolean {
  if (pipelineSaveCarriesPassword(mqttPassword)) return true;
  return JSON.stringify(server) !== JSON.stringify(draft);
}
