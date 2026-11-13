/* One accent word per heading: split a heading around it so the template can
 * wrap the middle part in `<em class="it">`. A heading with no accent comes
 * back whole. The accent must appear exactly once, as a whole word (so `stream`
 * does not match inside `Restreamer`), so a typo in a page's props fails the
 * build instead of silently losing the accent. */
export function splitAccent(heading: string, accent?: string): [string, string, string] {
  if (!accent) return [heading, "", ""];
  const word = new RegExp(`(?<![\\p{L}\\p{N}_])${accent.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?![\\p{L}\\p{N}_])`, "gu");
  const found = [...heading.matchAll(word)];
  if (found.length === 0) throw new Error(`splitAccent: "${accent}" is not in "${heading}"`);
  if (found.length > 1) throw new Error(`splitAccent: "${accent}" appears more than once in "${heading}"`);
  const at = found[0].index;
  return [heading.slice(0, at), accent, heading.slice(at + accent.length)];
}
