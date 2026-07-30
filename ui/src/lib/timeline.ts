/* ===========================================================================
   The timeline's pure data functions: timecode formatting, timecode parsing,
   and sprite-sheet placement.

   These lived in Timeline.tsx next to the component that draws them. They moved
   here because ClipEditor imports five of them and the component none of them:
   a page reaching into a component file for a string formatter is a hint the
   formatter was never part of the component.

   The mechanical reason is Fast Refresh. A file that exports both a component
   and a plain function cannot be hot-swapped -- React has no way to know
   whether re-running the module is safe -- so editing the timecode formatter
   used to full-reload the clip editor and lose the in/out points the operator
   had just dragged. Splitting the file is what makes that edit cheap.

   Nothing here touches the DOM or React, which is the test for whether
   something belongs in this file.
   =========================================================================== */

/** One sprite-sheet thumbnail, already placed on the timeline. Produced by
 *  parseSpriteVTT, which the page feeds from the recording's sprite.vtt. */
export interface SpriteCue {
  startMs: number;
  endMs: number;
  url: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// ---------------------------------------------------------------- formatting

/** HH:MM:SS.mmm — the timecode a broadcaster reads, at the precision a clip is
 *  cut to. Deliberately not shortDuration(): milliseconds are the whole point
 *  here, and a cut that reads as whole seconds hides its own drift. */
export function timecode(ms: number): string {
  const neg = ms < 0;
  const t = Math.abs(Math.round(ms));
  const h = Math.floor(t / 3600000);
  const m = Math.floor((t % 3600000) / 60000);
  const s = Math.floor((t % 60000) / 1000);
  const frac = t % 1000;
  return `${neg ? "-" : ""}${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(
    s,
  ).padStart(2, "0")}.${String(frac).padStart(3, "0")}`;
}

/** A short signed duration for drift readouts: "-1.84 s", "+120 ms". */
export function driftText(ms: number): string {
  const sign = ms < 0 ? "-" : "+";
  const abs = Math.abs(ms);
  if (abs < 1000) return `${sign}${Math.round(abs)} ms`;
  return `${sign}${(abs / 1000).toFixed(2)} s`;
}

/** Parses what a human types into a timecode field.
 *
 *  Accepts "90", "1:30", "01:02:03", any of them with a decimal fraction, and
 *  a bare "1:02.5". Returns null for anything else so the caller can leave the
 *  previous value alone rather than jumping the cut to zero. */
export function parseTimecode(text: string): number | null {
  const raw = text.trim();
  if (!raw) return null;
  const parts = raw.split(":");
  if (parts.length > 3) return null;
  let total = 0;
  for (const part of parts) {
    if (!/^\d*\.?\d*$/.test(part) || part === "" || part === ".") return null;
    const v = Number.parseFloat(part);
    if (!Number.isFinite(v)) return null;
    total = total * 60 + v;
  }
  return Math.round(total * 1000);
}

// ------------------------------------------------------------------- sprites

/** Turns a media sprite.vtt into placed thumbnails.
 *
 *  The cue payload is "sprite-001.jpg#xywh=x,y,w,h", relative to the VTT, so
 *  base is the URL prefix the sheets live under. offsetMs shifts a segment's
 *  own clock onto the stitched timeline, which is what lets an hour-two
 *  segment's thumbnails appear at hour two. */
export function parseSpriteVTT(text: string, base: string, offsetMs = 0): SpriteCue[] {
  const out: SpriteCue[] = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const arrow = lines[i].indexOf("-->");
    if (arrow < 0) continue;
    const start = parseVTTTime(lines[i].slice(0, arrow));
    const end = parseVTTTime(lines[i].slice(arrow + 3));
    const payload = (lines[i + 1] ?? "").trim();
    if (start === null || end === null || !payload) continue;
    const hash = payload.indexOf("#xywh=");
    if (hash < 0) continue;
    const nums = payload
      .slice(hash + 6)
      .split(",")
      .map((n) => Number.parseInt(n, 10));
    if (nums.length !== 4 || nums.some((n) => !Number.isFinite(n))) continue;
    out.push({
      startMs: start + offsetMs,
      endMs: end + offsetMs,
      url: base + payload.slice(0, hash),
      x: nums[0],
      y: nums[1],
      w: nums[2],
      h: nums[3],
    });
  }
  return out;
}

function parseVTTTime(s: string): number | null {
  const m = s.trim().match(/^(?:(\d+):)?(\d{1,2}):(\d{2})\.(\d{1,3})$/);
  if (!m) return null;
  const h = m[1] ? Number.parseInt(m[1], 10) : 0;
  return (
    h * 3600000 +
    Number.parseInt(m[2], 10) * 60000 +
    Number.parseInt(m[3], 10) * 1000 +
    Number.parseInt(m[4].padEnd(3, "0"), 10)
  );
}

/** The thumbnail covering a moment, or null when none does. */
export function spriteAt(sprites: SpriteCue[] | undefined, ms: number): SpriteCue | null {
  if (!sprites || sprites.length === 0) return null;
  // Linear from a binary search would be tidier, but a sprite list is one cue
  // per few seconds — a few hundred entries — and this runs on hover, not on
  // every frame.
  let best: SpriteCue | null = null;
  for (const c of sprites) {
    if (ms >= c.startMs && ms < c.endMs) return c;
    if (c.startMs <= ms) best = c;
  }
  return best;
}
