import type { TranslationKey, Translator } from "./i18n";

/**
 * Every action a schedule can carry, in the order the dropdown offers them.
 *
 * WHY THIS IS A MODULE AND NOT FOUR <SelectItem> LINES.
 *
 * The server and this list have to offer the same set: an action the server
 * accepts but the dropdown omits is a feature nobody can reach, and one the
 * dropdown offers but the server rejects is an error the operator cannot avoid.
 * That agreement used to be guarded from Go, by reading AutomationPage.tsx and
 * searching it for `<SelectItem value="...">`.
 *
 * Issue #107 is about why that could not work. A regexp over source text cannot
 * tell whether this file compiles, is satisfied by the string appearing inside
 * a comment, and -- as the guard's own note admitted -- would have failed the
 * moment anybody rewrote the dropdown as a `.map()`, even though mapping over a
 * list is the safer way to write it.
 *
 * So the list lives here, the dropdown renders from it, and the agreement is
 * checked against ui/src/lib/contract/scheduler-actions.json, which
 * internal/scheduler generates from the slice its own Validate ranges over.
 * scheduleActions.test.ts imports THIS array, so it cannot pass unless the
 * module compiles.
 */
export const SCHEDULE_ACTIONS = [
  "start",
  "stop",
  "playlist.start",
  "playlist.stop",
] as const;

export type ScheduleAction = (typeof SCHEDULE_ACTIONS)[number];

/**
 * The i18n key for each action's label.
 *
 * A Record keyed by ScheduleAction rather than a lookup that can miss: adding an
 * action to SCHEDULE_ACTIONS without a label here is a type error, which is the
 * one kind of drift check that costs nothing to run.
 */
export const SCHEDULE_ACTION_LABEL_KEYS: Record<ScheduleAction, TranslationKey> = {
  start: "auto.startDestinations",
  stop: "auto.stopDestinations",
  "playlist.start": "auto.startPlaylist",
  "playlist.stop": "auto.stopPlaylist",
};

/** Does this action act on the playlist rather than on destinations? */
export const isPlaylistAction = (a: ScheduleAction): boolean =>
  a.startsWith("playlist.");

/** Does this action turn something on? Mirrors the server's Enables(). */
export const isStartAction = (a: ScheduleAction): boolean =>
  a === "start" || a === "playlist.start";

/** The label to show for an action, translated. */
export const scheduleActionLabel = (a: ScheduleAction, t: Translator): string =>
  t(SCHEDULE_ACTION_LABEL_KEYS[a]);
