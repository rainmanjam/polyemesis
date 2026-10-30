// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { MixMatrix } from "./MixMatrix";
import type { MatrixCell, SourceTrack } from "@/lib/types";

/* A15: each matrix cell shows its state as a chip with a dB label, and the
 * number input stays the editing surface (arrow-key walking depends on it). */
afterEach(cleanup);

const tracks: SourceTrack[] = [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }];
const cells: MatrixCell[] = [
  { track: 0, channel: 0, out: 0, gain: 1 },
  { track: 0, channel: 1, out: 1, gain: 0.707 },
];

describe("MixMatrix cell chips", () => {
  it("labels a -3 dB cell with its dB value", () => {
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />);
    expect(screen.getByText("-3.0 dB")).toBeTruthy();
  });

  it("labels unity as 0.0 dB and an empty cell as off", () => {
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />);
    expect(screen.getByText("0.0 dB")).toBeTruthy();
    expect(screen.getAllByText("off").length).toBeGreaterThan(0);
  });

  it("exposes the state on the chip so it is readable without colour", () => {
    const { container } = render(
      <MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />,
    );
    const states = Array.from(container.querySelectorAll("[data-state]")).map((el) =>
      el.getAttribute("data-state"),
    );
    expect(states).toContain("unity");
    expect(states).toContain("custom");
    expect(states).toContain("off");
  });

  it("keeps the number input as the editing surface", () => {
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />);
    expect(screen.getAllByRole("spinbutton").length).toBe(4);
  });
});
