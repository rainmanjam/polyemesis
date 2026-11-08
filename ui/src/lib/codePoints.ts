/* The server counts and caps text in code points (Go runes), not UTF-16 units.
   `string.length` and the `maxLength` attribute count units, so an emoji would
   count twice and the counter would disagree with the server. These count and
   cut by code point, and never split a surrogate pair. */

export const cpLength = (s: string): number => [...s].length;

export function clampCp(s: string, max: number): string {
  const cps = [...s];
  return cps.length > max ? cps.slice(0, max).join("") : s;
}
