import { useCallback, useEffect, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import type { ChatLimit, ChatMessage, ChatPlatform, ChatStats, ChatStatus } from "@/lib/types";

/* ===========================================================================
   One socket for the whole app, refcounted, so mounting the dashboard panel and
   the chat page at once costs one connection rather than two. It is deliberately
   separate from useLiveData: chat is the only consumer of these two event types,
   and a page that never opens the pane should not pay to buffer its messages.

   The store is module state rather than a context because the refcount is the
   point -- two unrelated components mount and unmount independently, and a
   provider high enough to serve both would hold the socket open for pages that
   never show chat.
   =========================================================================== */

/** How many messages the browser keeps. The server bounds its own ring and its
 *  table; this bounds the DOM so a twelve-hour stream does not end with fifty
 *  thousand nodes on the page. */
const CLIENT_LIMIT = 600;

interface FeedState {
  connected: boolean;
  loading: boolean;
  /** False when the server has no chat hub wired at all. Distinct from an empty
   *  `statuses`, which means a hub is running with nothing attached. */
  configured: boolean;
  /** The scrollback came out of the database rather than a live connection. */
  stored: boolean;
  statuses: ChatStatus[];
  limits: ChatLimit[];
  messages: ChatMessage[];
  /** The hub's own counters. Only the overview carries them, so this is a
   *  snapshot from the last load rather than a live figure — the diagnostics
   *  block it feeds is refreshed by the reload button, not by every message. */
  stats: ChatStats | null;
  error: string;
}

const EMPTY: FeedState = {
  connected: false,
  loading: true,
  configured: false,
  stored: false,
  statuses: [],
  limits: [],
  messages: [],
  stats: null,
  error: "",
};

let feed: FeedState = EMPTY;
const listeners = new Set<() => void>();
let refs = 0;
let socket: WebSocket | null = null;
let retries = 0;
let reconnectTimer: number | undefined;
/** Dedupe across the socket and the initial fetch: the two overlap by exactly
 *  the messages that arrived while the fetch was in flight. */
let seen = new Set<string>();

function emit(patch: Partial<FeedState>) {
  feed = { ...feed, ...patch };
  listeners.forEach((l) => l());
}

/** One message's identity across a platform, an account and an id.
 *
 *  NUL as the separator rather than a colon or a space, because none of the
 *  three fields can contain it. An id containing the separator could otherwise
 *  collide with a different account's message, and a collision here does not
 *  throw -- it makes a real message silently vanish from the scrollback as a
 *  duplicate.
 *
 *  Written as the \u0000 ESCAPE and not as a raw NUL byte, which is what this
 *  file used to hold. git decides a file is binary by looking for a NUL in the
 *  first 8000 bytes, so a literal one here makes this source unreviewable --
 *  no diff, no blame, "Bin 0 -> 7031 bytes" in the commit. The escape is the
 *  same string at runtime and keeps the file ASCII.
 *
 *  Exported because the timeline keys its React rows off the same identity. Two
 *  definitions of "the same message" is how a list starts re-mounting rows. */
export const messageKey = (m: ChatMessage) =>
  `${m.platform}\u0000${m.account ?? ""}\u0000${m.id}`;

function appendMessage(m: ChatMessage) {
  const key = messageKey(m);
  if (seen.has(key)) return;
  seen.add(key);
  const next = [...feed.messages, m];
  emit({ messages: next.length > CLIENT_LIMIT ? next.slice(next.length - CLIENT_LIMIT) : next });
}

/** Drop one message locally after the platform accepted a delete. The platform
 *  is the authority; this only keeps our copy from outliving it on screen. */
function forgetMessage(m: { platform: ChatPlatform; account?: string; id: string }) {
  const key = `${m.platform}\u0000${m.account ?? ""}\u0000${m.id}`;
  seen.delete(key);
  emit({ messages: feed.messages.filter((x) => messageKey(x) !== key) });
}

async function loadHistory() {
  try {
    const view = await api.chatOverview(CLIENT_LIMIT);
    seen = new Set(view.messages.map(messageKey));
    // Anything the socket delivered while this was in flight is kept: the
    // fetch is the floor of the scrollback, not the whole of it.
    const live = feed.messages.filter((m) => !seen.has(messageKey(m)));
    live.forEach((m) => seen.add(messageKey(m)));
    emit({
      loading: false,
      error: "",
      configured: view.configured,
      stored: view.stored ?? false,
      statuses: view.statuses ?? [],
      limits: view.limits ?? [],
      stats: view.stats ?? null,
      messages: [...view.messages, ...live],
    });
  } catch (err) {
    emit({
      loading: false,
      error: err instanceof Error ? err.message : "Could not load chat.",
    });
  }
}

function openSocket() {
  if (socket || refs === 0) return;
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`);
  socket = ws;

  ws.onopen = () => {
    retries = 0;
    emit({ connected: true });
  };
  ws.onmessage = (ev) => {
    let msg: { type: string; data: unknown };
    try {
      msg = JSON.parse(ev.data as string) as { type: string; data: unknown };
    } catch {
      return;
    }
    if (msg.type === "chat") {
      appendMessage(msg.data as ChatMessage);
    } else if (msg.type === "chatState") {
      // A state event is proof a hub exists, whatever the last overview said.
      emit({ statuses: (msg.data as ChatStatus[]) ?? [], configured: true, stored: false });
    }
  };
  ws.onclose = () => {
    socket = null;
    emit({ connected: false });
    if (refs === 0) return;
    // Same backoff shape the rest of the app uses, so a restarting server is
    // not hammered by every open tab.
    const delay = Math.min(1000 * 2 ** retries, 15000);
    retries++;
    reconnectTimer = window.setTimeout(openSocket, delay);
  };
  ws.onerror = () => ws.close();
}

function acquire() {
  refs++;
  if (refs === 1) {
    feed = { ...EMPTY };
    seen = new Set();
    openSocket();
    void loadHistory();
  }
}

function release() {
  refs--;
  if (refs > 0) return;
  window.clearTimeout(reconnectTimer);
  const ws = socket;
  socket = null;
  ws?.close();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The shared chat feed. Refcounted: the first component to mount opens the
 *  socket, the last to unmount closes it. */
export function useChatFeed() {
  useEffect(() => {
    acquire();
    return release;
  }, []);
  const state = useSyncExternalStore(subscribe, () => feed);

  const reload = useCallback(() => {
    emit({ loading: true });
    return loadHistory();
  }, []);

  const remove = useCallback(async (m: ChatMessage) => {
    // The button is offered for every message on every platform. Whether a
    // platform supports deletion is the platform's answer to give, and hiding
    // the action on a guess would silently remove a moderator's only tool the
    // day a platform gains the capability.
    await api.deleteChatMessage({ platform: m.platform, account: m.account, id: m.id });
    forgetMessage(m);
  }, []);

  return { ...state, reload, remove };
}
