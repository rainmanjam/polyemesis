/** Parses "N. text" back into an ordered array, tolerating stray blank lines. */
export function parseNumbered(out, want) {
  const byIndex = new Map();
  for (const line of out.split("\n")) {
    const m = /^\s*(\d+)\s*[.)](.*)$/.exec(line);
    if (!m) continue;
    const n = Number(m[1]);
    if (n >= 1 && n <= want && !byIndex.has(n)) byIndex.set(n, m[2].trim());
  }
  return Array.from({ length: want }, (_, i) => byIndex.get(i + 1) ?? null);
}
