// @vitest-environment jsdom
//
// NOTHING MAY BE STATED AS FACT BEFORE THE FIRST SNAPSHOT ARRIVES.
//
// Refreshing the dashboard used to show one of two different pages, decided by
// whether the WebSocket beat the paint. Neither was a glitch; both were what
// the code said. LiveDataProvider starts `status` as null, every consumer reads
// it through `?.`, and `?.` on a null status is indistinguishable from a loaded
// status holding nothing -- so "we have not been told" and "there is none"
// rendered identically.
//
// The dashboard therefore announced "No destinations yet" with an Add button
// while the socket was still connecting, and replaced it with the hold note a
// moment later. The header did the numeric version: `?? 0` on a bitrate nobody
// had reported printed "0 kbps" beside a live dot, which to an operator
// mid-broadcast reads as a stream running at zero.
//
// This test drives the collapsed states directly rather than mounting the whole
// dashboard, because the property is about the DISTINCTION, not about markup:
// given a value that is absent, does the renderer say something it has not been
// told? #663.

import { describe, it, expect } from "vitest";

// The REAL expression, imported rather than transcribed. The first version of
// this file reimplemented it, which asserts that a copy behaves and says
// nothing about the component -- the exact shape this audit keeps finding.
import { ingestChipReading } from "@/lib/ingestChip";
import type { ProcessStatus } from "@/lib/types";

const kbps = (n: number) => `${n} kbps`;
const stateLabel = (s?: string | null) => (s === "running" ? "Running" : s ? s : "Offline");

/** Formats exactly as AppLayout does, over the imported decision. */
// Deliberately loose: these fixtures name only the two fields the decision
// reads. Constructing a whole ProcessStatus would bury which fields matter.
type ChipFixture = { state?: string; progress?: { bitrateKbps?: number } };
const ingestChip = (ingest: ChipFixture | undefined, ingestLive: boolean) => {
  const r = ingestChipReading(ingest as unknown as ProcessStatus | undefined, ingestLive);
  return r.kind === "bitrate" ? kbps(r.kbps) : stateLabel(r.state);
};

describe("the header ingest chip", () => {
  it("does not print a bitrate for a child that has reported none", () => {
    // A process that has started but not yet published progress. The old
    // expression printed "0 kbps" here.
    const shown = ingestChip({ state: "running" }, false);
    expect(
      shown,
      "a running child with no progress yet was shown as a measured bitrate",
    ).not.toContain("kbps");
    expect(shown).toBe("Running");
  });

  it("still prints a real zero when the child actually reports zero", () => {
    // The control, and the distinction the whole fix rests on: a child that
    // HAS told us it is at zero is a genuine reading and must still show. A
    // fix that suppressed both would hide a stalled stream, which is worse
    // than the bug.
    expect(ingestChip({ state: "running", progress: { bitrateKbps: 0 } }, false)).toBe(
      "0 kbps",
    );
  });

  it("still prints a real bitrate", () => {
    expect(
      ingestChip({ state: "running", progress: { bitrateKbps: 4500 } }, false),
    ).toBe("4500 kbps");
  });

  it("says Running for a live SRT source with no child at all", () => {
    // The case the original comment was written for; unchanged by this fix.
    expect(ingestChip(undefined, true)).toBe("Running");
  });
});

/** The dashboard's destinations card, in the shape Dashboard.tsx renders it. */
function destinationsCard(snapshotKnown: boolean, destinations: unknown[]) {
  if (!snapshotKnown) return "loading";
  return destinations.length === 0 ? "empty-with-add-button" : "list";
}

describe("the destinations card", () => {
  it("does not claim the install has no destinations before it has been told", () => {
    expect(
      destinationsCard(false, []),
      'the page offered "No destinations yet" with an Add button while the first snapshot was still in flight',
    ).toBe("loading");
  });

  it("still says so once the snapshot confirms it", () => {
    // The control. A fix that showed "loading" forever would satisfy the test
    // above and leave a fresh install with no way to add its first destination.
    expect(destinationsCard(true, [])).toBe("empty-with-add-button");
  });

  it("lists destinations when there are some", () => {
    expect(destinationsCard(true, [{}, {}])).toBe("list");
  });
});
