// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { DestinationHoldNote } from "./DestinationHoldNote";

/* WHY EVERY DESTINATION CARD IS GREY.
 *
 * The engine holds every destination while the ingest layout is unmeasured, and
 * a held destination has no Process — byte-for-byte what a CRASHED destination
 * looks like. The server computed the sentence explaining it (engine.HoldStatus,
 * on the wire as status.destinationHold, pinned by dest_hold_reason_test.go) and
 * `grep destinationHold ui/src` returned nothing at all.
 */

afterEach(cleanup);

describe("DestinationHoldNote", () => {
  it("renders the server's reason verbatim", () => {
    render(
      <DestinationHoldNote
        hold={{ code: "unmeasured", reason: "The ingest layout has not been measured yet." }}
      />,
    );
    expect(
      screen.getByText("The ingest layout has not been measured yet."),
    ).toBeTruthy();
  });

  it("renders nothing when the engine is planning destinations normally", () => {
    const { container } = render(<DestinationHoldNote hold={undefined} />);
    expect(container.querySelector("[data-testid='destination-hold']")).toBeNull();
  });

  it("renders nothing for a hold with a code and no sentence to show", () => {
    // `code` is for machinery; a person is shown `reason` or nothing.
    const { container } = render(<DestinationHoldNote hold={{ code: "unmeasured", reason: " " }} />);
    expect(container.querySelector("[data-testid='destination-hold']")).toBeNull();
  });
});
