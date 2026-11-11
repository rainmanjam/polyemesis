/** Strips "/" from the end of `s`, or from both ends. Plain index scans: the
 *  equivalent /\/+$/ regexes rescan a whole run of slashes from every starting
 *  position, which is quadratic on a hostile paste. */
export function trimTrailingSlashes(s: string): string {
  let end = s.length;
  while (end > 0 && s.codePointAt(end - 1) === 47) end--;
  return s.slice(0, end);
}

export function trimSlashes(s: string): string {
  let start = 0;
  while (start < s.length && s.codePointAt(start) === 47) start++;
  return trimTrailingSlashes(s.slice(start));
}
