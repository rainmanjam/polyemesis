import { useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import type { ChatPlatform } from "@/lib/types";

/* The tags the operator has put on viewers, for the small badge beside a name
 * in the chat list.
 *
 * ONE lookup for the whole list, not one request per message: the server's
 * tagged-viewers answer carries tags only, never a note's text. A failed read
 * (a token without access, a server older than the route) leaves the map empty,
 * so a chat list simply shows no badges. */

type TagMap = ReadonlyMap<string, readonly string[]>;

const EMPTY: TagMap = new Map();
let current: TagMap = EMPTY;
let started = false;
let inflight = false;
// A refresh asked for while a read is in flight (a note saved a moment after the
// read began) would otherwise be dropped, leaving the badge stale.
let pending = false;
const listeners = new Set<() => void>();

export const viewerTagKey = (platform: string, account: string | undefined, authorId: string) =>
  `${platform}|${account ?? ""}|${authorId}`;

function emit() {
  for (const l of listeners) l();
}

/** Re-reads the lookup. Called after a note is saved or deleted. */
export function refreshViewerTags(): void {
  if (inflight) {
    pending = true;
    return;
  }
  inflight = true;
  Promise.resolve()
    .then(() => api.viewerTags())
    .then((res) => {
      const next = new Map<string, readonly string[]>();
      for (const v of res?.viewers ?? []) {
        if (v.tags?.length) next.set(viewerTagKey(v.platform, v.account, v.authorId), v.tags);
      }
      current = next;
      emit();
    })
    .catch(() => {
      /* No badges; the card still works. */
    })
    .finally(() => {
      inflight = false;
      if (pending) {
        pending = false;
        refreshViewerTags();
      }
    });
}

function subscribe(l: () => void) {
  listeners.add(l);
  if (!started) {
    started = true;
    refreshViewerTags();
  }
  return () => {
    listeners.delete(l);
  };
}

/** The tags on one viewer, or an empty list. */
export function useViewerTags(platform: ChatPlatform, account: string | undefined, authorId: string | undefined): readonly string[] {
  const map = useSyncExternalStore(subscribe, () => current, () => EMPTY);
  if (!authorId) return [];
  return map.get(viewerTagKey(platform, account, authorId)) ?? [];
}

/** Test seam: forget what was learned. */
export function resetViewerTags(): void {
  current = EMPTY;
  started = false;
  inflight = false;
  pending = false;
}
