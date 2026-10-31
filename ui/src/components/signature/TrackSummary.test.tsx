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
    render(<TrackSummary tracks={[0]} labels={["Host mic"]} count={4} />);

    expect(screen.getByTitle("Track 1 (Host mic) is included")).toBeTruthy();
    // Beyond the end of `labels`, within the ingest's four tracks.
    expect(screen.getByTitle("Track 4 is excluded")).toBeTruthy();
  });

  it("draws one chip per ingest track: four for a four-track source, not six", () => {
    // The demo's Studio A carries four audio tracks; the dashboard drew six.
    render(<TrackSummary tracks={[0, 2]} count={4} />);

    expect(screen.getAllByTitle(/^Track \d+/)).toHaveLength(4);
    expect(screen.queryByTitle("Track 5 is excluded")).toBeNull();
    expect(screen.queryByTitle("Track 6 is excluded")).toBeNull();
  });

  it("keeps the wording it had when no labels are supplied at all", () => {
    // The multi-programme case: lib/trackLabels.ts returns null rather than
    // name another show's tracks, and this is what the operator then sees.
    render(<TrackSummary tracks={[1]} labels={null} />);

    expect(screen.getByTitle("Track 2 is included")).toBeTruthy();
    expect(screen.getByTitle("Track 1 is excluded")).toBeTruthy();
  });
});

/* app-council A89: the routed and excluded chips differed by fill colour
 * alone. An excluded chip is now an outline (dashed, no fill) and a routed one
 * is filled, a difference of shape that survives greyscale and colour-blindness,
 * and each chip says which it is in data-routed. */
describe("TrackSummary: routed versus excluded is not colour alone", () => {
  it("draws an excluded chip as a dashed outline and a routed chip filled", () => {
    render(<TrackSummary tracks={[0]} count={2} />);
    const on = screen.getByTitle("Track 1 is included");
    const off = screen.getByTitle("Track 2 is excluded");
    expect(on.getAttribute("data-routed")).toBe("true");
    expect(off.getAttribute("data-routed")).toBe("false");
    expect(off.className).toContain("border-dashed");
    expect(off.className).toContain("bg-transparent");
    expect(on.className).not.toContain("border-dashed");
  });
});
