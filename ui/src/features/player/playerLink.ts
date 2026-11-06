/** The public player's URL: who the viewer is (the playback token) and where
 *  in the stream they asked to start (a timestamp).
 *
 *  `?t=` MEANS TWO THINGS and that is not a choice made here. The media origin
 *  has always accepted the playback token as `?t=` (internal/api/playout.go's
 *  playoutTokenParam), and /watch has always fallen back to it, so a link of
 *  the form `/watch?t=<token>` exists in the wild and must keep working. A
 *  timestamp share link uses the same letter because that is what viewers know
 *  from every other player.
 *
 *  They are told apart by SHAPE. A timestamp is the moment as Unix seconds:
 *  9 to 11 digits and nothing else. A playback token is 32 random bytes in
 *  base64url -- 43 characters -- so a real token can never be all digits of
 *  that length. And when the token is in the path, which is the form the
 *  console hands out, `?t=` is only ever a timestamp. */

const TIMESTAMP = /^\d{9,11}$/;

export interface PlayerLink {
  token?: string;
  /** The requested moment, in Unix milliseconds. */
  atMs?: number;
}

/** Parse /watch, /watch/:token and either form with `?t=`. */
export function parsePlayerLink(pathname: string, search: string): PlayerLink {
  let path = pathname;
  while (path.endsWith("/")) path = path.slice(0, -1);
  const prefix = "/watch/";
  let token: string | undefined;
  if (path.startsWith(prefix)) {
    const raw = path.slice(prefix.length);
    if (raw) {
      try {
        token = decodeURIComponent(raw);
      } catch {
        token = raw;
      }
    }
  }
  const q = new URLSearchParams(search).get("t") ?? undefined;
  let atMs: number | undefined;
  if (q !== undefined && TIMESTAMP.test(q)) {
    atMs = Number(q) * 1000;
  } else if (q !== undefined && q !== "" && token === undefined) {
    token = q;
  }
  return { token, atMs };
}

/** A link to this page at a moment. The token stays in the path -- the form
 *  that survives being pasted -- so the query is free for the timestamp. */
export function shareLinkAt(origin: string, token: string | undefined, atMs: number): string {
  const path = token ? `/watch/${encodeURIComponent(token)}` : "/watch";
  return `${origin}${path}?t=${Math.floor(atMs / 1000)}`;
}

/** A wall-clock anchor: the media time at which a known instant played. */
export interface WallAnchor {
  wallMs: number;
  mediaTime: number;
}

/** Media time for a wall-clock instant, or undefined when it is outside the
 *  seekable window (the DVR window has moved past it, or it is in the future). */
export function mediaTimeFor(
  atMs: number,
  anchor: WallAnchor,
  range: { start: number; end: number },
): number | undefined {
  const t = anchor.mediaTime + (atMs - anchor.wallMs) / 1000;
  if (!Number.isFinite(t) || t < range.start || t > range.end) return undefined;
  return t;
}

/** The wall-clock instant playing at a media time. */
export function wallTimeAt(mediaTime: number, anchor: WallAnchor): number {
  return anchor.wallMs + (mediaTime - anchor.mediaTime) * 1000;
}

/** How far away the next stream is, in the largest unit that is at least one,
 *  for Intl.RelativeTimeFormat -- which is what makes "in 3 hours" read
 *  naturally in every locale. Undefined once the moment has passed. */
export function countdownUnit(
  targetMs: number,
  nowMs: number,
): { value: number; unit: "day" | "hour" | "minute" | "second" } | undefined {
  const s = Math.floor((targetMs - nowMs) / 1000);
  if (s <= 0) return undefined;
  if (s >= 86400) return { value: Math.floor(s / 86400), unit: "day" };
  if (s >= 3600) return { value: Math.floor(s / 3600), unit: "hour" };
  if (s >= 60) return { value: Math.floor(s / 60), unit: "minute" };
  return { value: s, unit: "second" };
}
