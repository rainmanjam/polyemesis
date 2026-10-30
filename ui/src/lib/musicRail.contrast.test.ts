import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { railTextClass } from "./musicRail";

/* The routing rail's secondary text on a SELECTED row sits on --primary-dim,
 * not on --card. --muted-foreground is 5.8:1 on the card and only 4.29:1 on
 * --primary-dim, under the 4.5:1 WCAG AA floor for small text. This test reads
 * the real tokens from index.css, so a token change that breaks the pair fails
 * here rather than in a contrast review. */

const CSS = readFileSync(new URL("../index.css", import.meta.url), "utf8");

function token(name: string): [number, number, number] {
  const m = CSS.match(new RegExp(`--${name}:\\s*oklch\\(([\\d.]+)%\\s+([\\d.]+)\\s+([\\d.]+)\\)`));
  if (!m) throw new Error(`token --${name} not found`);
  return [Number(m[1]) / 100, Number(m[2]), Number(m[3])];
}

function luminance([L, C, h]: [number, number, number]): number {
  const a = C * Math.cos((h * Math.PI) / 180);
  const b = C * Math.sin((h * Math.PI) / 180);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const lin = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ].map((x) => Math.max(0, Math.min(1, x)));
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2];
}

function ratio(fg: string, bg: string): number {
  const a = luminance(token(fg));
  const b = luminance(token(bg));
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

const tokenOf = (cls: string) => cls.replace(/^text-/, "");

describe("routing rail text contrast", () => {
  it("the measurement reproduces the failing pair it replaces", () => {
    // Positive control: the old class on the selected fill really was under AA.
    expect(ratio("muted-foreground", "primary-dim")).toBeLessThan(4.5);
  });

  for (const tone of ["muted", "warn"] as const) {
    it(`a selected row's ${tone} text clears 4.5:1 on --primary-dim`, () => {
      expect(ratio(tokenOf(railTextClass(tone, true)), "primary-dim")).toBeGreaterThanOrEqual(4.5);
    });
    it(`an unselected row's ${tone} text clears 4.5:1 on --card`, () => {
      expect(ratio(tokenOf(railTextClass(tone, false)), "card")).toBeGreaterThanOrEqual(4.5);
    });
  }

  it("an unselected row keeps its quiet colour", () => {
    expect(railTextClass("muted", false)).toBe("text-muted-foreground");
    expect(railTextClass("warn", false)).toBe("text-warn");
    expect(railTextClass("warn", true)).toBe("text-warn");
  });
});
