import { describe, expect, it } from "vitest";

import { authorInk } from "./authorColor";

describe("authorInk on a hostile functional colour", () => {
  // The functional-colour regex used to carry an overlapping \s* and [^)]*,
  // which backtracked quadratically on a long run of whitespace with no ")".
  // A chat author colour comes from the platform, so this is attacker-shaped
  // input: it must come back unchanged, and quickly.
  it("returns a long unterminated rgb( string untouched and fast", () => {
    const hostile = "rgb(" + " ".repeat(50_000) + "1";
    const started = performance.now();
    const out = authorInk(hostile, "light");
    const elapsed = performance.now() - started;
    expect(out).toBe(hostile);
    expect(elapsed).toBeLessThan(500);
  });

  it("still reads a well-formed functional colour with irregular spacing", () => {
    const dark = authorInk("hsl(60  100%,50%)", "light");
    expect(dark).not.toBe("hsl(60  100%,50%)");
    expect(dark.startsWith("#")).toBe(true);
  });
});
