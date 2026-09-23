/* The ONE place a /api/v1/ws URL is built.
 *
 * The server resolves the socket's engine through scopedEngine, the same
 * device GET /status uses, and refuses an unnamed upgrade with 400
 * source_required on any install with two or more sources. LiveDataProvider
 * knew this and named its programme; useChatFeed did not, and opened a bare
 * `/api/v1/ws` that failed and reconnected forever on every multi-source
 * install while its REST scrollback loaded fine.
 *
 * `programme` is REQUIRED, and `null` is a value a caller must pass on
 * purpose: it is the honest answer on an install with no sources, where the
 * route accepts it. There is no default to fall back on, because a default is
 * exactly how the chat socket came to say nothing. A test in
 * liveSocket.test.ts refuses any `new WebSocket(` in src/ that does not go
 * through this function. */
export function liveSocketUrl(programme: number | null): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const q = programme == null ? "" : `?source=${encodeURIComponent(String(programme))}`;
  return `${proto}//${location.host}/api/v1/ws${q}`;
}
