import type { TranslationKey } from "./i18n";
import type { Destination, RoutingProfile, TrackSel } from "./types";

/* Live per-track mute and gain on the routing page (I4).
 *
 * POST /destinations/{id}/tracks/{track}/gain writes the STORED live mix and
 * reconciles the destination, which applies a level-only change on the running
 * FFmpeg's command channel instead of restarting it. These helpers decide, from
 * the stored row, whether a track can take that route, and turn the server's
 * answer into what the operator reads.
 *
 * Availability is read off the STORED destination, never the draft: the route
 * changes what is stored, so a track the operator has ticked but not saved is
 * not there yet and the server would answer 404. */

/** The change a live control sends. At least one field is set. */
export interface LevelChange {
  muted?: boolean;
  gain?: number;
}

/** The answer of POST /destinations/{id}/tracks/{track}/gain. */
export interface TrackLevelResult {
  applied: "live" | "restart";
  reason?: string;
  destination: Destination;
  warnings?: string[];
}

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

/** Why one track cannot take a live level, or null when it can. */
export function trackLiveBlock(
  stored: Pick<Destination, "kind" | "audio" | "profile">,
  track: number,
): TranslationKey | null {
  const whole = destinationLiveBlock(stored);
  if (whole) return whole;
  // A stored matrix mix refuses a gain, and these controls live in the simple
  // view; until the simple mix is saved there is nothing for them to change.
  if (stored.profile.mode !== "simple") return "route.live.saveFirst";
  if (!(stored.profile.tracks ?? []).some((s) => s.track === track)) return "route.live.saveFirst";
  return null;
}

/** The draft with one track's level changed and nothing else.
 *
 *  Spreads the existing row, so every other field (enabled, and muted when only
 *  the gain moves) survives. A track the draft does not hold is left alone:
 *  the stored row carries the change, and the draft's own selection is the
 *  operator's unsaved decision. */
export function withLevel(profile: RoutingProfile, track: number, change: LevelChange): RoutingProfile {
  if (!(profile.tracks ?? []).some((s) => s.track === track)) return profile;
  return {
    ...profile,
    tracks: profile.tracks.map((s): TrackSel => (s.track === track ? { ...s, ...change } : s)),
  };
}

/** What the operator is told after a live change. */
export interface LevelOutcome {
  /** "quiet" when the level reached the running output; "warn" when it took,
   *  or will take, a restart. */
  tone: "quiet" | "warn";
  key: TranslationKey;
  reason: string;
  warnings: string[];
}

export function levelOutcome(res: Pick<TrackLevelResult, "applied" | "reason" | "warnings">): LevelOutcome {
  const reason = res.reason ?? "";
  const warnings = res.warnings ?? [];
  if (res.applied === "live") {
    return {
      tone: warnings.length ? "warn" : "quiet",
      key: reason ? "route.live.appliedLiveNote" : "route.live.appliedLive",
      reason,
      warnings,
    };
  }
  return {
    tone: "warn",
    key: reason ? "route.live.appliedRestart" : "route.live.appliedRestartBare",
    reason,
    warnings,
  };
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
