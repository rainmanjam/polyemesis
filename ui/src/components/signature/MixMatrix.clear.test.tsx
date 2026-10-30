// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { MixMatrix } from "./MixMatrix";
import type { MatrixCell, SourceTrack } from "@/lib/types";

/* THE CLEAR IS UNDOABLE RULE (A14). Clear drops every connection in the mix in
 * one click. The mix is a live routing edit, and a mis-click used to lose the
 * whole matrix with no way back short of rebuilding it by hand. Clear now
 * keeps what it removed and offers an Undo right beside it until the operator
 * edits again. */
afterEach(cleanup);

const tracks: SourceTrack[] = [{ index: 0, channels: 2, codec: "aac", layout: "stereo" }];
const cells: MatrixCell[] = [
  { track: 0, channel: 0, out: 0, gain: 1 },
  { track: 0, channel: 1, out: 1, gain: 0.707 },
];

function Host({ initial, seen }: { initial: MatrixCell[]; seen: MatrixCell[][] }) {
  const [mix, setMix] = useState<MatrixCell[]>(initial);
  return (
    <MixMatrix
      tracks={tracks}
      cells={mix}
      onChange={(next) => {
        seen.push(next);
        setMix(next);
      }}
    />
  );
}

describe("MixMatrix Clear", () => {
  it("empties the matrix when Clear is pressed", () => {
    const onChange = vi.fn();
    render(<MixMatrix tracks={tracks} cells={cells} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: /clear/i }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it("offers Undo after Clear, and Undo restores the removed connections", () => {
    // The parent hands back the list it was given, as RoutingPage does, so Undo
    // is judged against the list Clear produced.
    const seen: MatrixCell[][] = [];
    render(<Host initial={cells} seen={seen} />);
    fireEvent.click(screen.getByRole("button", { name: /clear/i }));
    fireEvent.click(screen.getByRole("button", { name: /undo/i }));
    expect(seen[seen.length - 1]).toEqual(cells);
  });

  it("offers no Undo before anything has been cleared", () => {
    render(<MixMatrix tracks={tracks} cells={cells} onChange={() => {}} />);
    expect(screen.queryByRole("button", { name: /undo/i })).toBeNull();
  });

  it("withdraws Undo when the parent replaces the mix, so old cells are not written back", () => {
    // Clear on one destination, then a preset, a discard or a switch to another
    // destination hands the matrix new cells from outside. Undo must not then
    // restore the cleared cells over them.
    const { rerender } = render(<Host initial={cells} seen={[]} />);
    fireEvent.click(screen.getByRole("button", { name: /clear/i }));
    expect(screen.getByRole("button", { name: /undo/i })).toBeTruthy();
    // A new list from outside (preset, discard, or another destination).
    rerender(
      <MixMatrix
        tracks={tracks}
        cells={[{ track: 0, channel: 0, out: 0, gain: 0.5 }]}
        onChange={() => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: /undo/i })).toBeNull();
  });
});
