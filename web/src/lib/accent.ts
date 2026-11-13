/* One accent word per heading: split a heading around it so the template can
 * wrap the middle part in `<em class="it">`. A heading with no accent comes
 * back whole. The accent must appear exactly once, so a typo in a page's
 * props fails the build instead of silently losing the accent. */
export function splitAccent(heading: string, accent?: string): [string, string, string] {
  if (!accent) return [heading, "", ""];
  const at = heading.indexOf(accent);
  if (at < 0) throw new Error(`splitAccent: "${accent}" is not in "${heading}"`);
  if (heading.indexOf(accent, at + accent.length) >= 0) {
    throw new Error(`splitAccent: "${accent}" appears more than once in "${heading}"`);
  }
  return [heading.slice(0, at), accent, heading.slice(at + accent.length)];
}
