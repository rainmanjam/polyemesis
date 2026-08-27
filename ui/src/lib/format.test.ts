import { describe, expect, it } from "vitest";
import { timestamp } from "./format";

describe("timestamp and the zero time", () => {
  it("renders nothing for Go's unset time rather than a date in the year 1", () => {
    // The automation page rendered this as "12/31/1, 16:07:02" -- the zero
    // pushed backwards across a local-time offset -- beside six counters all
    // reading 0. A caller guarding on truthiness cannot tell it from a real
    // timestamp, because it is a non-empty string that parses cleanly.
    expect(timestamp("0001-01-01T00:00:00Z")).toBe("");
  });

  it("still renders a real timestamp", () => {
    // The control. A fix that returned "" for everything would satisfy the
    // case above and blank every date in the product.
    expect(timestamp("2026-08-26T21:15:00Z")).not.toBe("");
  });

  it("renders nothing for empty or unparseable input", () => {
    expect(timestamp("")).toBe("");
    expect(timestamp("not a date")).toBe("");
  });
});
