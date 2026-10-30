// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { MixMatrix } from "./MixMatrix";
import type { MatrixCell, SourceTrack } from "@/lib/types";

/* A15: a matrix cell's polarity toggle and a per-track mute. Polarity is a cell
 * field (a negative pan coefficient on the server); mute is the track's level. */
afterEach(cleanup);

const tracks: SourceTrack[] = [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }];
const cells: MatrixCell[] = [
  { track: 0, channel: 0, out: 0, gain: 1 },
  { track: 0, channel: 1, out: 1, gain: 0.5, invert: true },
];

const invertButtons = () => screen.getAllByRole("button", { name: /^Invert polarity/ });

describe("MixMatrix polarity", () => {
  it("offers the toggle only on a connected cell, and shows the inverted one as pressed and in words", () => {
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />);
    const btns = invertButtons();
    expect(btns).toHaveLength(2);
    expect(btns.map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "true"]);
    expect(screen.getByText(/-6\.0 dB inv/)).toBeTruthy();
  });

  it("toggling sets and clears the field on that cell only", () => {
    const onChange = vi.fn();
    render(<MixMatrix tracks={tracks} cells={cells} onChange={onChange} />);
    fireEvent.click(invertButtons()[0]);
    expect(onChange).toHaveBeenLastCalledWith([
      { track: 0, channel: 0, out: 0, gain: 1, invert: true },
      cells[1],
    ]);
    fireEvent.click(invertButtons()[1]);
    // Cleared as an explicit false, never by dropping the key.
    // MUTATION: un-invert by destructuring the key away. Observed: FAIL.
    expect(onChange).toHaveBeenLastCalledWith([cells[0], { track: 0, channel: 1, out: 1, gain: 0.5, invert: false }]);
  });

  // MUTATION: rebuild the cell without carrying `invert` in setGain. Observed: FAIL.
  it("a new gain keeps the cell's polarity", () => {
    const onChange = vi.fn();
    render(<MixMatrix tracks={tracks} cells={cells} onChange={onChange} />);
    const input = screen.getByLabelText("Track 1 channel 2 to right");
    fireEvent.change(input, { target: { value: "0.8" } });
    const next = onChange.mock.lastCall?.[0] as MatrixCell[];
    expect(next.find((c) => c.channel === 1)).toEqual({ track: 0, channel: 1, out: 1, gain: 0.8, invert: true });
  });
});

describe("MixMatrix explicit un-invert", () => {
  // MUTATION: carry only a true invert in setGain. Observed: FAIL.
  it("a new gain keeps an explicit invert false", () => {
    const onChange = vi.fn();
    const off: MatrixCell[] = [{ track: 0, channel: 1, out: 1, gain: 0.5, invert: false }];
    render(<MixMatrix tracks={tracks} cells={off} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText("Track 1 channel 2 to right"), { target: { value: "0.8" } });
    const next = onChange.mock.lastCall?.[0] as MatrixCell[];
    expect(next).toEqual([{ track: 0, channel: 1, out: 1, gain: 0.8, invert: false }]);
  });
});

describe("MixMatrix track mute", () => {
  it("toggles a track's mute without touching any cell", () => {
    const onChange = vi.fn();
    const onMute = vi.fn();
    render(<MixMatrix tracks={tracks} cells={cells} onChange={onChange} muted={[]} onMute={onMute} />);
    fireEvent.click(screen.getByRole("button", { name: "Mute track 1" }));
    expect(onMute).toHaveBeenCalledWith(0, true);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("a muted track reads as muted and offers unmute", () => {
    const onMute = vi.fn();
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} muted={[0]} onMute={onMute} />);
    const btn = screen.getByRole("button", { name: "Unmute track 1" });
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.textContent).toMatch(/muted/);
    fireEvent.click(btn);
    expect(onMute).toHaveBeenCalledWith(0, false);
  });
});
