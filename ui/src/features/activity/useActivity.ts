import { useCallback, useEffect, useSyncExternalStore } from "react";
import { api, ApiError } from "@/lib/api";
import { liveSocketUrl } from "@/lib/liveSocket";
import { useLiveData } from "@/hooks/useLiveData";
import type { ActivityEvent } from "@/lib/types";
import { FEED_LIMIT, mergeEvents } from "./activityLib";

/* ===========================================================================
   The activity feed: seeded from GET /activity, then live from the /ws socket's
   `activity` frames. The same shape as useChatFeed -- a refcounted module store,
   so the tab and anything else that mounts it share one socket -- but a
   separate socket, opened only while the Activity tab is on screen: a console
   that never opens the tab does not pay to buffer its events.
   =========================================================================== */

/** The first read asks for this many; the socket and later reads add to it. */
const SEED = 200;

interface FeedState {
  /** Newest first, de-duplicated by id, at most FEED_LIMIT. */
  events: ActivityEvent[];
  /** Whether the server is receiving activity at all (settings.activity.enabled).
   *  null until the first read answers. */
  enabled: boolean | null;
  loading: boolean;
  /** A read-scoped session is refused (403): the feed needs an admin. */
  forbidden: boolean;
  error: string;
  connected: boolean;
  /** A frame arrived that could not be parsed and was dropped. Sticky, like
   *  the chat feed's: the socket stays open, so nothing else would say so. */
  frameError: boolean;
}

const EMPTY: FeedState = {
  events: [],
  enabled: null,
  loading: true,
  forbidden: false,
  error: "",
  connected: false,
  frameError: false,
};

let feed: FeedState = EMPTY;
const listeners = new Set<() => void>();
let refs = 0;
let socket: WebSocket | null = null;
let retries = 0;
let reconnectTimer: number | undefined;
/** See useChatFeed: undefined is "not answered yet" and opens nothing. */
let programme: number | null | undefined;
/** Whether the socket has dropped since it first opened. A socket that comes
 *  back after a drop missed events, so it reads the list again to fill the gap. */
let dropped = false;

function emit(patch: Partial<FeedState>) {
  feed = { ...feed, ...patch };
  listeners.forEach((l) => l());
}

async function load() {
  try {
    const view = await api.getActivity({ limit: SEED });
    emit({
      loading: false,
      error: "",
      forbidden: false,
      enabled: view.enabled,
      // Merged, not replaced: frames that arrived while this was in flight stay.
      events: mergeEvents(feed.events, view.events ?? [], FEED_LIMIT),
    });
  } catch (err) {
    if (err instanceof ApiError && err.status === 403) {
      emit({ loading: false, forbidden: true, error: "" });
      return;
    }
    emit({ loading: false, error: err instanceof Error ? err.message : "Could not load activity." });
  }
}

function openSocket() {
  if (socket || refs === 0 || programme === undefined) return;
  const ws = new WebSocket(liveSocketUrl(programme));
  socket = ws;
  ws.onopen = () => {
    if (socket !== ws) return;
    retries = 0;
    emit({ connected: true });
    if (dropped) {
      dropped = false;
      void load();
    }
  };
  ws.onmessage = (ev) => {
    if (socket !== ws) return;
    let msg: { type: string; data: unknown };
    try {
      msg = JSON.parse(ev.data as string) as { type: string; data: unknown };
    } catch {
      emit({ frameError: true });
      return;
    }
    if (msg.type === "activity" && msg.data && typeof msg.data === "object") {
      emit({ events: mergeEvents(feed.events, [msg.data as ActivityEvent], FEED_LIMIT) });
    }
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    dropped = true;
    emit({ connected: false });
    if (refs === 0) return;
    const delay = Math.min(1000 * 2 ** retries, 15000);
    retries++;
    reconnectTimer = window.setTimeout(openSocket, delay);
  };
  ws.onerror = () => ws.close();
}

function aim(next: number | null) {
  if (programme === next) return;
  programme = next;
  window.clearTimeout(reconnectTimer);
  retries = 0;
  const ws = socket;
  socket = null;
  if (ws) {
    ws.close();
    emit({ connected: false });
  }
  openSocket();
}

function acquire() {
  refs++;
  if (refs === 1) {
    feed = { ...EMPTY };
    dropped = false;
    void load();
  }
}

function release() {
  refs--;
  if (refs > 0) return;
  window.clearTimeout(reconnectTimer);
  const ws = socket;
  socket = null;
  programme = undefined;
  ws?.close();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export type ReplayOutcome = "replayed" | "gone";

/** The shared activity feed. Refcounted: the first component to mount opens the
 *  socket, the last to unmount closes it. */
export function useActivity() {
  const { programme: current, programmeKnown } = useLiveData();
  useEffect(() => {
    acquire();
    return release;
  }, []);
  useEffect(() => {
    if (programmeKnown) aim(current);
  }, [programmeKnown, current]);
  const state = useSyncExternalStore(subscribe, () => feed);

  const reload = useCallback(() => {
    emit({ loading: true });
    return load();
  }, []);

  /** Draws a held alert on the overlay again. "gone" is the server's 404: it
   *  no longer holds that alert (or it was never an overlay kind). Anything
   *  else is thrown for the caller to report. */
  const replay = useCallback(async (id: string): Promise<ReplayOutcome> => {
    try {
      await api.replayActivity(id);
      return "replayed";
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return "gone";
      throw err;
    }
  }, []);

  return { ...state, reload, replay };
}
