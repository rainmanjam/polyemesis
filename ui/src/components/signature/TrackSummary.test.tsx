// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { TrackSummary } from "@/components/signature/TrackRows";

afterEach(cleanup);

/* THE WIRING, not the wording.
 *
 * trackChipTitle is unit-tested in lib/trackLabels.test.ts; what cannot be
 * covered there is whether TrackSummary actually reaches for the label at the
 * right INDEX. That is the part that fails silently: an off-by-one here would
 * put "Music bed" on the mic's chip, and every tooltip would still look
 * plausible because they all read as sentences about tracks.
 */
describe("TrackSummary", () => {
  it("names each chip from the label at its own index", () => {
    render(
      <TrackSummary
        tracks={[0, 2]}
        labels={["Host mic", "Music bed", "Commentary"]}
      />,
    );

    expect(screen.getByTitle("Track 1 (Host mic) is included")).toBeTruthy();
    // Track 2 is NOT in the mix, and its tooltip still names it -- an operator
    // asking "why is the music missing?" needs the excluded chip to say what
    // it is, not just that it is off.
    expect(screen.getByTitle("Track 2 (Music bed) is excluded")).toBeTruthy();
    expect(screen.getByTitle("Track 3 (Commentary) is included")).toBeTruthy();
  });

  it("falls back to the plain wording for tracks nobody has named", () => {
    render(<TrackSummary tracks={[0]} labels={["Host mic"]} />);

    expect(screen.getByTitle("Track 1 (Host mic) is included")).toBeTruthy();
    // Beyond the end of `labels`, and the six chips are always drawn.
    expect(screen.getByTitle("Track 4 is excluded")).toBeTruthy();
  });

  it("keeps the wording it had when no labels are supplied at all", () => {
    // The multi-programme case: lib/trackLabels.ts returns null rather than
    // name another show's tracks, and this is what the operator then sees.
    render(<TrackSummary tracks={[1]} labels={null} />);

    expect(screen.getByTitle("Track 2 is included")).toBeTruthy();
    expect(screen.getByTitle("Track 1 is excluded")).toBeTruthy();
  });
});
