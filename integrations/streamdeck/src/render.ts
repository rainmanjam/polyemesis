import type { KeyFace, KeyTone } from "./keyFace.js";
import { t } from "./i18n.js";

// What a key shows. The manifest declares one state per entry here for every key
// action, in this order, each with its own image (the colour family). The state index is the only thing
// the device draws, so the order here and the order in manifest.json must
// match. test/manifest.test.ts enforces that.

export const KEY_STATE = {
  idle: 0,
  live: 1,
  warn: 2,
  error: 3,
  held: 4,
  unbound: 5,
  halted: 6,
  needsAdmin: 7,
  muted: 8,
  restarted: 9,
  unavailable: 10,
} as const satisfies Record<KeyTone, number>;

export function stateFor(tone: KeyTone): number {
  return KEY_STATE[tone];
}

/** Keys are 72 px. A title past this many characters is cut, with an ellipsis. */
export const TITLE_MAX = 28;

export function clip(text: string, max = TITLE_MAX): string {
  const chars = [...text.trim()];
  return chars.length <= max ? chars.join("") : `${chars.slice(0, max - 1).join("")}…`;
}

/**
 * The text under a destination key: its state, the classified cause, then the server's words, clipped.
 * With more than one issue open, " +N" ends the reason line (or the label, when there is none), and the
 * line is clipped short enough that the badge itself is never what is cut.
 */
export function titleFor(face: KeyFace, locale: string): string {
  const badge = face.more && face.more > 0 ? ` +${face.more}` : "";
  const label = t(locale, face.label, face.labelVars);
  const lines = [face.reason || !badge ? label : `${clip(label, TITLE_MAX - badge.length)}${badge}`];
  if (face.reason) lines.push(`${clip(t(locale, face.reason, face.reasonVars), TITLE_MAX - badge.length)}${badge}`);
  if (face.detail) lines.push(clip(face.detail));
  if (face.warning) lines.push(clip(t(locale, face.warning)));
  if (face.delaySeconds) lines.push(clip(t(locale, "streamdeck.delay.marker", { seconds: face.delaySeconds })));
  return lines.join("\n");
}

/**
 * Every current issue of a destination key, one line each, most severe first, then its warning: what the
 * property inspector lists, because a key can show only one and cannot be hovered. Unclipped: the
 * inspector has the room the key does not.
 */
export function issueLines(face: KeyFace, locale: string): string[] {
  const lines = (face.issues ?? []).map((i) => {
    const what = t(locale, i.key, i.vars);
    const head = i.state ? `${t(locale, i.state, i.stateVars)}: ${what}` : what;
    const extra = [i.detail, i.note].filter((x): x is string => typeof x === "string" && x !== "");
    return extra.length > 0 ? `${head}: ${extra.join(" — ")}` : head;
  });
  if (face.warning) {
    const w = t(locale, face.warning);
    lines.push(face.warningDetail ? `${w}: ${face.warningDetail}` : w);
  }
  return lines;
}

/** A stop key's text: the hold instruction, then what a completed hold would end. */
export function stopTitle(count: number | undefined, locale: string): string {
  const hold = t(locale, "streamdeck.stop.hold");
  if (count === undefined) return [hold, t(locale, "streamdeck.stop.countFailed")].join("\n");
  const detail = count > 0 ? t(locale, "streamdeck.stop.youtube", { count }) : t(locale, "streamdeck.stop.noYoutube");
  return [hold, detail].join("\n");
}

/**
 * What a destination key's inspector lists. A destination's own face lists its issues; a face that is not
 * about the destination at all (not paired, unreachable, a refused token) is the one line that explains
 * why there is no list, so an unreachable install never reads as "nothing reported".
 */
export function inspectorLines(face: KeyFace, locale: string): string[] {
  if (face.issues || face.warning) return issueLines(face, locale);
  if (face.label.startsWith("streamdeck.state.")) return [];
  return [titleFor(face, locale).split("\n").join(": ")];
}
