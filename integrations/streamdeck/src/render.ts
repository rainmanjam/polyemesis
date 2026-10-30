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

/** The text under a destination key: its state, the classified cause, then the server's words, clipped. */
export function titleFor(face: KeyFace, locale: string): string {
  const lines = [t(locale, face.label)];
  if (face.reason) lines.push(clip(t(locale, face.reason)));
  if (face.detail) lines.push(clip(face.detail));
  if (face.warning) lines.push(clip(t(locale, face.warning)));
  if (face.delaySeconds) lines.push(clip(t(locale, "streamdeck.delay.marker", { seconds: face.delaySeconds })));
  return lines.join("\n");
}

/** A stop key's text: the hold instruction, then what a completed hold would end. */
export function stopTitle(count: number | undefined, locale: string): string {
  const hold = t(locale, "streamdeck.stop.hold");
  if (count === undefined) return [hold, t(locale, "streamdeck.stop.countFailed")].join("\n");
  const detail = count > 0 ? t(locale, "streamdeck.stop.youtube", { count }) : t(locale, "streamdeck.stop.noYoutube");
  return [hold, detail].join("\n");
}
