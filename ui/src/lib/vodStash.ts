import type { DestinationId, RoutingProfile } from "./types";

/* The second mix the operator switched off, kept for this browser tab (A14).
 *
 * Switching the second mix off drops its profile, and switching it back on
 * should be an undo, not a fresh copy of the live mix. That stash used to live
 * in a ref, so a reload in between lost the exclusions, gains and loudness
 * target the operator had set. sessionStorage keeps it for the tab, keyed by
 * destination so one destination's stash is never offered on another, and no
 * longer than the tab: it is an undo, not a saved setting.
 *
 * Storage that throws (Safari private mode) degrades to no stash, never to a
 * broken toggle. */

const key = (id: DestinationId) => `polyemesis.vodStash.${id}`;

export function readVodStash(id: DestinationId): RoutingProfile | null {
  try {
    const raw = sessionStorage.getItem(key(id));
    return raw ? (JSON.parse(raw) as RoutingProfile) : null;
  } catch {
    return null;
  }
}

export function writeVodStash(id: DestinationId, profile: RoutingProfile | null): void {
  try {
    if (profile) sessionStorage.setItem(key(id), JSON.stringify(profile));
    else sessionStorage.removeItem(key(id));
  } catch {
    // Kept in memory by the caller; only the reload case is lost.
  }
}
