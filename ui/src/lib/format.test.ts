import { afterEach, describe, expect, it } from "vitest";
import { displayTimeZone, setDisplayTimeZone, timestamp } from "./format";

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

describe("the install display time zone", () => {
  afterEach(() => setDisplayTimeZone("UTC"));

  it("renders every clock in the configured zone, not the browser's", () => {
    // The defect this replaces: an operator in Los Angeles read a server log
    // line in UTC beside the screen that produced it, and did the arithmetic
    // in their head while something was going wrong. Two people on the same
    // production could not read each other's screenshots.
    setDisplayTimeZone("UTC");
    const utc = timestamp("2026-08-27T05:51:00Z");
    setDisplayTimeZone("Australia/Sydney");
    const syd = timestamp("2026-08-27T05:51:00Z");
    expect(syd).not.toBe(utc);
    // Sydney is well ahead of UTC, so the same instant is a later hour there.
    expect(syd).toContain("15:51");
    expect(utc).toContain("05:51");
  });

  it("falls back to UTC, never to the browser, when the zone is empty", () => {
    // NOT the browser's zone. A console whose times move depending on who is
    // looking at it is the thing being fixed, so the default install must not
    // keep that behaviour.
    setDisplayTimeZone("");
    expect(displayTimeZone()).toBe("UTC");
    setDisplayTimeZone(null);
    expect(displayTimeZone()).toBe("UTC");
  });

  it("survives a zone this browser cannot resolve", () => {
    // The server validates on save, so this only happens when the two
    // disagree -- an old browser against a zone database the server has. UTC
    // is the honest answer; throwing would take every clock down with it.
    setDisplayTimeZone("Mars/Olympus_Mons");
    expect(displayTimeZone()).toBe("UTC");
    expect(() => timestamp("2026-08-27T05:51:00Z")).not.toThrow();
  });
});
