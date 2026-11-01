import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import {
  YOUTUBE_VERTICAL_POLL_MS,
  YOUTUBE_VERTICAL_TIERS,
  primaryDestinations, unconfirmedVerticalPairs, verticalHalfOf } from "./youtubeVertical";
import type { DestStatus, DestinationId } from "@/lib/types";

const id = (n: number) => n as DestinationId;

// The tier keys are the server's. This reads the Go source and fails if the list here names a
// tier the server does not have, or leaves one out, so the two cannot drift silently.
describe("the vertical tiers match the server", () => {
  it("lists exactly the tiers internal/db/youtube_vertical_tiers.go defines, in order", () => {
    const src = readFileSync(
      new URL("../../../internal/db/youtube_vertical_tiers.go", import.meta.url),
      "utf8",
    );
    const serverKeys = [...src.matchAll(/tier\("([a-z0-9]+)"/g)].map((m) => m[1]);
    expect(serverKeys.length).toBeGreaterThan(0);
    expect([...YOUTUBE_VERTICAL_TIERS]).toEqual(serverKeys);
  });
});

function row(p: { id: number } & Partial<Omit<DestStatus, "id">>): DestStatus {
  return { name: `d${p.id}`, kind: "rtmp", platform: "youtube", enabled: true, ...p, id: id(p.id) } as DestStatus;
}

describe("the vertical health poll respects the quota floor", () => {
  it("polls no faster than once a minute", () => {
    expect(YOUTUBE_VERTICAL_POLL_MS).toBeGreaterThanOrEqual(60_000);
  });
});

describe("a vertical half is one card, not a second one", () => {
  const horiz = row({ id: 1, youtubeVertical: { verticalId: id(2), pairingConfirmed: false } });
  const vert = row({ id: 2, verticalHalfOf: id(1) });
  const other = row({ id: 3 });

  it("draws the horizontal and unrelated rows as cards and hides the vertical half", () => {
    expect(primaryDestinations([horiz, vert, other]).map((d) => d.id)).toEqual([id(1), id(3)]);
  });

  it("finds the vertical row a horizontal card carries", () => {
    expect(verticalHalfOf(horiz, [horiz, vert, other])?.id).toBe(id(2));
  });

  it("does not draw a phantom half when the link names a row that is not listed", () => {
    expect(verticalHalfOf(horiz, [horiz, other])).toBeUndefined();
  });

  it("counts only the pairs still waiting for the Live Control Room step", () => {
    const confirmed = row({ id: 4, youtubeVertical: { verticalId: id(5), pairingConfirmed: true } });
    expect(unconfirmedVerticalPairs([horiz, confirmed, other])).toBe(1);
    expect(unconfirmedVerticalPairs([other])).toBe(0);
  });
});
