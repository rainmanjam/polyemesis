// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";

import { RouteOverview, type OverviewRow } from "./RouteOverview";

/* A4: the Overview grid shows every destination against every track. Clicking a
 * cell edits only the selected destination (the shared draft the Simple view also
 * edits); other rows select first. The amber cell is a music track reaching a
 * VOD mix, and it says so in words, not only in colour. */
afterEach(cleanup);

const tracks = [
  { index: 0, title: "Host mic" },
  { index: 1, title: "Music bed" },
];

const rows: OverviewRow[] = [
  {
    id: 1,
    name: "YouTube — full mix",
    tone: "live",
    kind: "editable",
    routed: [0, 1],
    vodBound: true,
    vodRouted: [1],
    guardedMusic: false,
    vodGuardedMusic: false,
  },
  {
    id: 2,
    name: "Twitch — no music",
    tone: "live",
    kind: "select",
    routed: [0],
    vodBound: false,
    vodRouted: [],
    guardedMusic: true,
    vodGuardedMusic: false,
  },
  {
    id: 3,
    name: "Archive — hosts only",
    tone: "idle",
    kind: "matrix",
    routed: [0],
    vodBound: false,
    vodRouted: [],
    guardedMusic: false,
    vodGuardedMusic: false,
  },
];

function renderGrid(over: Partial<Parameters<typeof RouteOverview>[0]> = {}) {
  const props = {
    rows,
    tracks,
    music: [1],
    onToggle: vi.fn(),
    onSelect: vi.fn(),
    onFix: vi.fn(),
    ...over,
  };
  render(<RouteOverview {...props} />);
  return props;
}

describe("RouteOverview music-rights state", () => {
  it("names the amber row's music reaching VOD in words", () => {
    renderGrid();
    expect(screen.getByText("Music reaches VOD")).toBeTruthy();
  });

  it("says both mixes in the amber cell name: what the live mix does, and the VOD risk", () => {
    renderGrid();
    expect(
      screen.getByRole("button", { name: "Music bed to YouTube — full mix, routed, music reaches VOD" }),
    ).toBeTruthy();
  });

  it("marks the amber cell VOD in words, not only in colour", () => {
    renderGrid();
    const cell = screen.getByRole("button", { name: /Music bed to YouTube — full mix.*music reaches VOD/i });
    expect(within(cell).getByText("VOD")).toBeTruthy();
  });
});

describe("RouteOverview editing", () => {
  it("toggles a cell on the selected destination", () => {
    const props = renderGrid();
    fireEvent.click(screen.getByRole("button", { name: /Host mic to YouTube — full mix/i }));
    expect(props.onToggle).toHaveBeenCalledWith(0);
    expect(props.onSelect).not.toHaveBeenCalled();
  });

  it("selects a destination instead of editing it when its row is not selected", () => {
    const props = renderGrid();
    fireEvent.click(screen.getByRole("button", { name: /Host mic to Twitch/i }));
    expect(props.onSelect).toHaveBeenCalledWith(2);
    expect(props.onToggle).not.toHaveBeenCalled();
  });

  it("leaves a matrix-mode destination's cells read-only", () => {
    renderGrid();
    const cell = screen.getByRole("button", { name: /Host mic to Archive/i });
    expect((cell as HTMLButtonElement).disabled).toBe(true);
  });

  it("offers the fix on the selected row's music hit, and calls it", () => {
    const props = renderGrid();
    fireEvent.click(screen.getByRole("button", { name: /Remove from YouTube — full mix/i }));
    expect(props.onFix).toHaveBeenCalledWith(1);
  });
});
