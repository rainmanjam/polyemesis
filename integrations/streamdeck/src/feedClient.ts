import tls from "node:tls";
import WebSocket from "ws";
import type { LevelsFrame } from "./model.js";
import { levelsFromMessage } from "./levelsFeed.js";

export interface FeedOptions {
  base: URL;
  token: string;
  /** The install's CA, pinned in addition to the system roots. */
  caPem?: string;
  onFrame: (frame: LevelsFrame) => void;
}

const BACKOFF_MIN_MS = 1000;
const BACKOFF_MAX_MS = 30_000;

/**
 * Keeps one WebSocket open to the install and hands each levels frame to
 * onFrame. Reconnects with backoff. Returns a stop function.
 *
 * The same bearer token authenticates the socket as the REST calls, and a
 * read-scoped token may receive levels (internal/api/ws_policy.go).
 */
export function startFeed(opts: FeedOptions): () => void {
  const url = new URL(opts.base.toString());
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = `${opts.base.pathname.replace(/\/$/, "")}/api/v1/ws`;
  url.search = "";
  const ca = opts.caPem && opts.caPem.trim() !== "" ? [...tls.rootCertificates, opts.caPem] : undefined;

  let socket: WebSocket | undefined;
  let stopped = false;
  let delay = BACKOFF_MIN_MS;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const open = () => {
    if (stopped) return;
    socket = new WebSocket(url, {
      headers: { Authorization: `Bearer ${opts.token}` },
      ca,
      rejectUnauthorized: true,
      handshakeTimeout: 5000,
    });
    socket.on("open", () => {
      delay = BACKOFF_MIN_MS;
    });
    socket.on("message", (data) => {
      const frame = levelsFromMessage(data.toString());
      if (frame) opts.onFrame(frame);
    });
    const retry = () => {
      if (stopped) return;
      timer = setTimeout(open, delay);
      delay = Math.min(delay * 2, BACKOFF_MAX_MS);
    };
    socket.on("error", () => undefined);
    socket.on("close", retry);
  };

  open();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    socket?.terminate();
  };
}
