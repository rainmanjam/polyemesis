import { describe, expect, it } from "vitest";

import { parseTimecode } from "./timeline";

describe("parseTimecode", () => {
  it.each([
    ["90", 90_000],
    ["1:30", 90_000],
    ["01:02:03", 3_723_000],
    ["1:02.5", 62_500],
    ["0.25", 250],
    [".5", 500],
    ["5.", 5000],
  ])("reads %s", (text, ms) => {
    expect(parseTimecode(text)).toBe(ms);
  });

  it.each(["", ".", "1:.", "1::2", "1:2:3:4", "a", "1.2.3", "-5", "1e3", "1,5"])("rejects %j", (text) => {
    expect(parseTimecode(text)).toBeNull();
  });

  it("rejects a pathological field without backtracking", () => {
    const t0 = performance.now();
    expect(parseTimecode(`${"1".repeat(100_000)}x`)).toBeNull();
    expect(parseTimecode(`${".".repeat(100_000)}`)).toBeNull();
    expect(performance.now() - t0).toBeLessThan(250);
  });
});
