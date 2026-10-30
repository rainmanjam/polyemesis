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

/** The level the server STORED for one track, read off its answer.
 *
 *  Not the request's own value: the server may hold something else (a clamp,
 *  or another client's change that landed first), and the form must show what
 *  is stored. Both fields go out explicit, so merging it cannot leave an old
 *  mute behind. Falls back to the request only when the answer does not carry
 *  the track, which a well-formed answer always does. */
export function storedLevel(res: Pick<TrackLevelResult, "destination">, track: number, sent: LevelChange): LevelChange {
  const row = (res.destination?.profile?.tracks ?? []).find((s) => s.track === track);
  if (!row) return sent;
  return { muted: row.muted === true, gain: row.gain };
}

/** Orders the answers of live level requests, per key.
 *
 *  Each request takes a number; an answer is accepted only when it is newer
 *  than every answer already accepted for that key. A slower, earlier answer
 *  that lands after a later one is dropped, so it cannot put an old level back
 *  on screen. A later request that FAILS does not strand the earlier answer:
 *  it was never accepted, so the earlier one still lands. */
export class LevelSequencer {
  private issued = new Map<string, number>();
  private accepted = new Map<string, number>();

  next(key: string): number {
    const n = (this.issued.get(key) ?? 0) + 1;
    this.issued.set(key, n);
    return n;
  }

  /** True, and recorded, when `seq` is newer than any accepted answer for `key`. */
  accept(key: string, seq: number): boolean {
    if (seq <= (this.accepted.get(key) ?? 0)) return false;
    this.accepted.set(key, seq);
    return true;
  }

  /** True while `seq` is the newest request issued for `key`. */
  latest(key: string, seq: number): boolean {
    return this.issued.get(key) === seq;
  }
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
