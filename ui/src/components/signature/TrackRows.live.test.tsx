// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

import { TrackRows, type TrackRowsLive } from "./TrackRows";
import type { SourceTrack, TrackSel } from "@/lib/types";

/* I4 on the routing page: a live mute and gain per track. They go to the
 * per-track route through `live`, never through `onChange`, because onChange
 * marks the draft dirty and is sent by Save. */
afterEach(cleanup);

beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterAll(() => vi.unstubAllGlobals());

const tracks: SourceTrack[] = [
  { index: 0, channels: 2, codec: "aac", layout: "stereo" },
  { index: 1, channels: 2, codec: "aac", layout: "stereo" },
];
const selection: TrackSel[] = [
  { track: 0, enabled: true, gain: 1 },
  { track: 1, enabled: true, gain: 0.5, muted: true },
];

function live(over: Partial<TrackRowsLive> = {}): TrackRowsLive {
  return {
    blocked: () => null,
    onMute: vi.fn(async () => {}),
    onGain: vi.fn(async () => {}),
    ...over,
  };
}

function renderRows(l?: TrackRowsLive, onChange = vi.fn()) {
  render(
    <TrackRows
      idPrefix="live"
      tracks={tracks}
      selection={selection}
      probed
      onChange={onChange}
      live={l}
    />,
  );
  return onChange;
}

describe("TrackRows live mute and gain", () => {
  it("renders no mute control without the live capability, but still shows a stored mute", () => {
    renderRows(undefined);
    expect(screen.queryByRole("button", { name: /mute track/i })).toBeNull();
    expect(screen.getByTestId("muted-1").textContent).toMatch(/muted/);
  });

  it("a mute press goes to the live route and leaves the draft alone", async () => {
    const l = live();
    const onChange = renderRows(l);
    const btn = screen.getByRole("button", { name: "Mute track 1" });
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    await act(async () => fireEvent.click(btn));
    expect(l.onMute).toHaveBeenCalledWith(0, true);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("a muted track offers unmute and reads as pressed", async () => {
    const l = live();
    renderRows(l);
    const btn = screen.getByRole("button", { name: "Unmute track 2" });
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    await act(async () => fireEvent.click(btn));
    expect(l.onMute).toHaveBeenCalledWith(1, false);
  });

  it("a committed gain goes to the live route, not the draft", async () => {
    const l = live();
    const onChange = renderRows(l);
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    expect(l.onGain).toHaveBeenCalledWith(0, 0.95);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("the live gain never sends zero: silence is the mute", async () => {
    const l = live();
    renderRows(l);
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "Home" }));
    expect(l.onGain).toHaveBeenCalledWith(0, 0.05);
  });

  it("a blocked track keeps the draft slider, disables mute, and says why under the row", async () => {
    const l = live({
      blocked: (track) => (track === 0 ? { reason: "Save first.", inline: true } : null),
    });
    const onChange = renderRows(l);
    const btn = screen.getByRole("button", { name: "Mute track 1" });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("live-block-0").textContent).toBe("Save first.");
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    expect(l.onGain).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenCalled();
  });

  it("a reason that holds for every row is not repeated under each one", () => {
    renderRows(live({ blocked: () => ({ reason: "Copy.", inline: false }) }));
    expect(screen.queryByTestId("live-block-0")).toBeNull();
    for (const b of screen.getAllByRole("button", { name: /mute track/i })) {
      expect((b as HTMLButtonElement).disabled).toBe(true);
      expect(b.getAttribute("title")).toBe("Copy.");
    }
  });
});
