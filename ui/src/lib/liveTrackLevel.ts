import type { TranslationKey } from "./i18n";
import type { Destination, RoutingProfile, TrackSel } from "./types";

/* Mute and gain on the routing page. Both are draft edits applied by Save (the
 * console no longer writes a level live; the Stream Deck still does, through its
 * own client), so what is left here is the pure helpers around the draft. */

/** The lowest gain the live slider sends. The route refuses zero and below:
 *  silence is a mute, which keeps the track in the graph. */
export const LIVE_MIN_GAIN = 0.05;

/** Why a destination as a whole cannot take a live level, or null when it can.
 *  Mirrors the route's 409: an audio-copy destination forwards its audio
 *  untouched, so there is no volume stage for a level to reach. Icecast
 *  ("audio") rows encode, so copy does not block them. */
export function destinationLiveBlock(
  stored: Pick<Destination, "kind" | "audio">,
): TranslationKey | null {
  if (stored.audio?.copy && (stored.kind as string) !== "audio") return "route.live.copyBlocked";
  return null;
}

/** A preset's profile with the mutes the operator already has carried over.
 *
 *  A preset replaces the whole mix, and its tracks arrive unmuted. A mute is a
 *  live decision about what is on air right now, not part of a mix's shape, so
 *  applying a preset and saving must not quietly put a muted track back on
 *  air. Only tracks present in both keep their mute. */
export function carryMutes(next: RoutingProfile, prev: RoutingProfile | null): RoutingProfile {
  const muted = new Set((prev?.tracks ?? []).filter((s) => s.muted).map((s) => s.track));
  if (muted.size === 0) return next;
  return {
    ...next,
    tracks: (next.tracks ?? []).map((s) => (muted.has(s.track) ? { ...s, muted: true } : s)),
  };
}

/** A track selection with one track's mute set, for the matrix view (A15).
 *
 *  The matrix draws its routing in cells, but a mute is a level on the track's
 *  volume stage and applies in matrix mode too, so it is kept on the track row.
 *  A track the selection does not hold is added disabled: in matrix mode the
 *  cells decide what is routed, and the row only carries the mute. */
export function setMuted(tracks: TrackSel[], track: number, muted: boolean): TrackSel[] {
  if (tracks.some((s) => s.track === track)) {
    return tracks.map((s) => (s.track === track ? { ...s, muted } : s));
  }
  return [...tracks, { track, enabled: false, gain: 1, muted }].sort((a, b) => a.track - b.track);
}
