import type { TranslationKey } from "./i18n";

/** What the music mark beside a destination in the routing rail means.
 *
 *  It used to be a bare note glyph in `text-live` or `text-warn`, with no
 *  title, no aria-label and no word next to it -- in a product where green
 *  means ON AIR on every other screen and in the rail's own status dots. So the
 *  one mark that means "this destination excludes the music role" read as "this
 *  destination is live", and the amber one -- which means the opposite, that
 *  music is going out to a platform whose policy says it should not -- read as
 *  a warning about the stream's health.
 *
 *  Colour is not the message. The word is, and the colour only ranks it.
 *
 *  `guarded` is what the destination's own profile does: the music role is
 *  excluded from its mix. `policyExcludes` is what the platform's music-rights
 *  row asks for. The interesting case is the second without the first.
 */
export interface MusicMark {
  label: TranslationKey;
  /** "muted" for a statement of fact, "warn" for a divergence from policy. */
  tone: "muted" | "warn";
}

export function musicRailMark(guarded: boolean, policyExcludes: boolean): MusicMark | null {
  if (guarded) return { label: "route.railNoMusic", tone: "muted" };
  if (policyExcludes) return { label: "route.railMusicOn", tone: "warn" };
  return null;
}
