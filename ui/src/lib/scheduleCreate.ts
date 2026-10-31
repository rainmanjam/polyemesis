import { isPlaylistAction, type ScheduleAction } from "./scheduleActions";
import type { TranslationKey } from "./i18n";

/* Whether a schedule may be saved at all, and if not, why.
 *
 * TWO ways the dialog used to accept a schedule that could not do what it
 * said, both of them a success toast over a schedule that will never fire the
 * way the operator meant:
 *
 *   - A ONE-SHOT SEEDED AT THE EPOCH. `new Date(0).toISOString()` survives the
 *     server's Validate, which only rejects Go's zero time, and unixOrZero
 *     then stores 0 -- which time.Unix(0,0) reads back as unset. The schedule
 *     saves, toasts, and can never fire. Nothing on the dialog says so.
 *   - A DESTINATION LIST THAT NEVER ARRIVED. The checkbox list is drawn from
 *     `status?.destinations ?? []`, so before the first live snapshot it is
 *     empty and nothing distinguishes that from "an install with no
 *     destinations". The paragraph above it promises "none selected means
 *     every destination", and scheduler.go implemented exactly that -- so a
 *     `stop` schedule created in that window turned an unanswered question
 *     into "stop the whole broadcast at 23:00".
 *   - A DESTINATION SCHEDULE THAT NAMES NOTHING (app-council A54). An empty
 *     list used to MEAN every destination. Now "every destination" is the
 *     explicit All destinations tick (`allDestinations` on the wire), and a
 *     start or stop with neither a tick nor a pick is refused here and by the
 *     server alike.
 *
 * A CONTROL, not a warning: the Create button is disabled and the reason sits
 * beside it. Warning about the epoch would leave the operator free to save the
 * thing anyway, and the whole failure is that they had no reason to doubt it.
 *
 * A plain function so the rules are testable without mounting a dialog, and so
 * adding a fourth rule cannot be done by adding a fourth `&&` to a JSX
 * expression nobody can read. */

/** Only the fields the decision reads. `Schedule` satisfies it structurally,
 *  which keeps this module out of the pages' import graph. */
export interface ScheduleFormFacts {
  name: string;
  action: ScheduleAction;
  kind: "once" | "daily" | "weekly";
  /** RFC3339, or "" for a one-shot whose instant has not been chosen. */
  runAt: string;
  destinationIds: readonly number[] | null;
  /** The explicit "every destination" (app-council A54). Optional because a
   *  schedule read from a server older than the field has none. */
  allDestinations?: boolean;
}

export type ScheduleBlock =
  | { kind: "ok" }
  | { kind: "name" }
  | { kind: "destinationsUnknown"; reason: TranslationKey }
  | { kind: "destinationsNone"; reason: TranslationKey }
  | { kind: "runAtUnset"; reason: TranslationKey }
  | { kind: "runAtPast"; reason: TranslationKey };

/** Whether a one-shot's instant is genuinely absent.
 *
 *  The epoch counts as absent. It is not a time anybody chose -- it is what
 *  `new Date(0)` produced when the dialog seeded a blank form -- and the
 *  server stores it as "unset", so treating it as a real instant is how the
 *  page came to promise a fire that cannot happen. */
export function runAtUnset(runAt: string): boolean {
  if (!runAt) return true;
  const at = new Date(runAt).getTime();
  return Number.isNaN(at) || at <= 0;
}

/** The first reason this schedule may not be saved, or `ok`.
 *
 *  `destinationsKnown` is false while the live snapshot has not arrived. It
 *  only blocks CREATION, and only when All destinations is off: an existing
 *  schedule already carries its stored destination ids, and the save
 *  round-trips them untouched. */
export function scheduleBlock(
  form: ScheduleFormFacts,
  opts: { destinationsKnown: boolean; editing: boolean; now: number },
): ScheduleBlock {
  if (!form.name.trim()) return { kind: "name" };

  if (form.kind === "once") {
    if (runAtUnset(form.runAt)) {
      return { kind: "runAtUnset", reason: "auto.blockRunAtUnset" };
    }
    if (new Date(form.runAt).getTime() <= opts.now) {
      return { kind: "runAtPast", reason: "auto.blockRunAtPast" };
    }
  }

  if (!isPlaylistAction(form.action) && !form.allDestinations) {
    if (!opts.editing && !opts.destinationsKnown) {
      return { kind: "destinationsUnknown", reason: "auto.blockDestinationsUnknown" };
    }
    // On an edit too: clearing the last tick of an existing stop is the
    // likelier way to arrive here, and the server refuses it either way.
    if ((form.destinationIds ?? []).length === 0) {
      return { kind: "destinationsNone", reason: "auto.blockDestinationsNone" };
    }
  }

  return { kind: "ok" };
}

/** What switching the action does to the destination choice (app-council A54).
 *
 *  - A playlist action names no destination, so both the list and All are
 *    cleared: a stale pick riding along invisibly is a 400 from the server.
 *  - An All destinations ticked for a START does not carry into a STOP. Every
 *    destination on a stop takes the whole broadcast down, so it is ticked for
 *    the stop itself or not at all. The reverse (stop to start) keeps it: the
 *    consequence only gets smaller.
 *
 *  A function rather than an inline handler so the rule is testable without
 *  driving a Radix Select, which jsdom cannot open. */
export function formForAction(
  form: ScheduleFormFacts,
  action: ScheduleAction,
): { action: ScheduleAction; destinationIds: number[]; allDestinations: boolean } {
  const ids = [...(form.destinationIds ?? [])];
  if (isPlaylistAction(action)) return { action, destinationIds: [], allDestinations: false };
  const intoStop = action === "stop" && form.action !== "stop";
  return { action, destinationIds: ids, allDestinations: intoStop ? false : Boolean(form.allDestinations) };
}
