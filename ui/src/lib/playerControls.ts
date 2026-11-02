/** The public player's control decisions, as pure functions of what hls.js
 *  and the <video> element report. PublicPlayer.tsx wires them to events;
 *  keeping the decisions here is what lets them be tested without a browser
 *  that can play video. */

/** The fields of an hls.js Level these functions read. */
export interface LevelLike {
  name?: string;
  height?: number;
  url?: string[];
}

/** Which hls.js levels to remove for rungs the browser cannot decode, highest
 *  index first so each removal leaves the earlier indexes valid. A level is a
 *  rung when its NAME matches or its playlist URL is `<name>/index.m3u8`.
 *
 *  Never every level: hls.js with no levels plays nothing, and
 *  playableVariants has already decided whether falling back is possible --
 *  if this would empty the ladder, the last one is kept. */
export function levelsToRemove(levels: LevelLike[], dropped: ReadonlySet<string>): number[] {
  if (dropped.size === 0) return [];
  const out: number[] = [];
  for (let i = levels.length - 1; i >= 0; i--) {
    const lv = levels[i];
    const url = lv.url?.[0] ?? "";
    const named = [...dropped].some((n) => lv.name === n || url.endsWith(`/${n}/index.m3u8`));
    if (named) out.push(i);
  }
  if (out.length >= levels.length) out.pop();
  return out;
}

export interface QualityOption {
  index: number;
  label: string;
}

/** The quality menu's entries: a height where the rung has one, its name
 *  otherwise, and its name whenever two rungs share a height. */
export function qualityOptions(levels: LevelLike[]): QualityOption[] {
  const heights = new Map<number, number>();
  for (const lv of levels) if (lv.height) heights.set(lv.height, (heights.get(lv.height) ?? 0) + 1);
  return levels.map((lv, index) => ({
    index,
    label: lv.height && heights.get(lv.height) === 1 ? `${lv.height}p` : lv.name || `${index + 1}`,
  }));
}

/** Seconds behind the live edge past which "Go live" is offered; at the edge
 *  it would be a button that does nothing. */
export const BEHIND_LIVE_SECONDS = 8;

/** A seekable window shorter than this is a live playlist's own few segments,
 *  not a replay window, and a slider over it would only confuse. */
export const MIN_DVR_SECONDS = 30;

export interface PlayRange {
  start: number;
  end: number;
  /** The playhead. */
  at: number;
  /** Where "live" is: hls.js's liveSyncPosition, or the window's end. */
  edge: number;
}

export function isBehindLive(r: PlayRange | null): boolean {
  return !!r && r.edge - r.at > BEHIND_LIVE_SECONDS;
}

export function hasDvrWindow(r: PlayRange | null): boolean {
  return !!r && r.end - r.start >= MIN_DVR_SECONDS;
}
