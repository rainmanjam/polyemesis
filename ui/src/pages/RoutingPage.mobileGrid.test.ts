/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

/* ROUTING AT 390PX SCROLLED SIDEWAYS INSIDE ITSELF (2026-11 review, row 25).
 *
 * Below lg the picker/editor grid had no column template, so its one implicit
 * column was `auto`: sized to the min-content of the widest card (a
 * destination name plus its music-policy chip), 448px in a 356px pane. The
 * pane scrolled horizontally and clipped the Save button. A minmax(0,1fr)
 * column lets the cards shrink to the pane and truncate inside it. */

const SRC = readFileSync(fileURLToPath(new URL("./RoutingPage.tsx", import.meta.url)), "utf8");

describe("Routing's picker/editor grid", () => {
  it("has a shrinkable single column below lg", () => {
    expect(SRC).toContain('"grid grid-cols-1 gap-3 lg:grid-cols-[16rem_minmax(0,1fr)]"');
  });

  // The mute-and-gain tool row (Label tracks, Clip protection, its 14rem
  // select) was one unwrapping row, 422px wide in the 356px card.
  it("lets the mute-and-gain tool row wrap and its select shrink", () => {
    expect(SRC).toContain('<div className="flex min-w-0 flex-wrap items-center gap-2">');
    expect(SRC).toContain('className="w-56 max-w-full"');
  });
});
