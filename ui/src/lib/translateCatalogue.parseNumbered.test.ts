import { describe, expect, it } from "vitest";

import { parseNumbered } from "../../scripts/parse-numbered.mjs";

describe("translate-catalogue parseNumbered", () => {
  it("reads N. and N) lines in order, tolerating blank lines and padding", () => {
    const out = "\n 1. Hola\n\n2)  Adiós  \n  3 .tres\n";
    expect(parseNumbered(out, 3)).toEqual(["Hola", "Adiós", "tres"]);
  });

  it("leaves a hole for a missing, out-of-range or repeated index", () => {
    const out = "1. a\n1. dup\n4. out of range\n3. c";
    expect(parseNumbered(out, 3)).toEqual(["a", null, "c"]);
  });

  it("keeps an empty translation as empty text", () => {
    expect(parseNumbered("1.   ", 1)).toEqual([""]);
  });

  it("skips lines with no numbering", () => {
    expect(parseNumbered("hello\n1x. no\n", 1)).toEqual([null]);
  });

  it("finishes fast on a line of padding with no marker", () => {
    const t0 = performance.now();
    parseNumbered(`1${" ".repeat(200_000)}x\n${" ".repeat(200_000)}`, 1);
    parseNumbered(`1.${" ".repeat(200_000)}`, 1);
    expect(performance.now() - t0).toBeLessThan(250);
  });
});
