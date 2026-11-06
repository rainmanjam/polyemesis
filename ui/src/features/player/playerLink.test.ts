import { describe, expect, it } from "vitest";
import { countdownUnit, mediaTimeFor, parsePlayerLink, shareLinkAt, wallTimeAt } from "./playerLink";

// A real playback token: 32 random bytes, base64url, 43 characters.
const TOKEN = "playback-token-for-tests-only_xxxxxxxxxxxxx";

describe("parsePlayerLink", () => {
  it("reads the token from the path and a timestamp from ?t=", () => {
    expect(parsePlayerLink(`/watch/${TOKEN}`, "?t=1793541600")).toEqual({
      token: TOKEN,
      atMs: 1793541600_000,
    });
  });

  it("reads a timestamp alone on an open stream", () => {
    expect(parsePlayerLink("/watch", "?t=1793541600")).toEqual({ token: undefined, atMs: 1793541600_000 });
  });

  it("still accepts the legacy query-string token", () => {
    expect(parsePlayerLink("/watch", `?t=${TOKEN}`)).toEqual({ token: TOKEN, atMs: undefined });
  });

  it("never reads a 43-character token as a timestamp", () => {
    const digits = "1".repeat(43);
    expect(parsePlayerLink("/watch", `?t=${digits}`)).toEqual({ token: digits, atMs: undefined });
  });

  it("ignores a query that is not a timestamp when the path has the token", () => {
    expect(parsePlayerLink(`/watch/${TOKEN}/`, "?t=yesterday")).toEqual({ token: TOKEN, atMs: undefined });
  });

  it("decodes an escaped path token and tolerates a malformed escape", () => {
    expect(parsePlayerLink("/watch/a%2Db", "").token).toBe("a-b");
    expect(parsePlayerLink("/watch/a%E0%A4%A", "").token).toBe("a%E0%A4%A");
  });

  it("has no token or moment on a bare /watch", () => {
    expect(parsePlayerLink("/watch", "")).toEqual({ token: undefined, atMs: undefined });
  });
});

describe("shareLinkAt", () => {
  it("puts the token in the path and whole seconds in ?t=", () => {
    const link = shareLinkAt("https://tv.example", TOKEN, 1793541600_999);
    expect(link).toBe(`https://tv.example/watch/${TOKEN}?t=1793541600`);
    // And the page reads its own link back as the same token and moment.
    const u = new URL(link);
    expect(parsePlayerLink(u.pathname, u.search)).toEqual({ token: TOKEN, atMs: 1793541600_000 });
  });

  it("has no path token on an open stream", () => {
    expect(shareLinkAt("https://tv.example", undefined, 1793541600_000)).toBe(
      "https://tv.example/watch?t=1793541600",
    );
  });
});

describe("wall clock <-> media time", () => {
  const anchor = { wallMs: 1_000_000, mediaTime: 100 };
  const window = { start: 40, end: 160 };

  it("maps an instant inside the window", () => {
    expect(mediaTimeFor(1_030_000, anchor, window)).toBe(130);
    expect(wallTimeAt(130, anchor)).toBe(1_030_000);
  });

  it("refuses an instant the DVR window has moved past, or one in the future", () => {
    expect(mediaTimeFor(900_000, anchor, window)).toBeUndefined();
    expect(mediaTimeFor(1_100_000, anchor, window)).toBeUndefined();
  });
});

describe("countdownUnit", () => {
  const now = 1_000_000_000;
  it.each([
    [2 * 86400 + 3 * 3600, { value: 2, unit: "day" }],
    [3 * 3600 + 12 * 60, { value: 3, unit: "hour" }],
    [12 * 60 + 30, { value: 12, unit: "minute" }],
    [45, { value: 45, unit: "second" }],
  ])("%is away", (secs, want) => {
    expect(countdownUnit(now + secs * 1000, now)).toEqual(want);
  });
  it("is undefined once the moment has come", () => {
    expect(countdownUnit(now, now)).toBeUndefined();
    expect(countdownUnit(now - 5000, now)).toBeUndefined();
  });
});
