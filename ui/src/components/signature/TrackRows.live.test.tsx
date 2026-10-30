// @vitest-environment jsdom

import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

import { LIVE_GAIN_DEBOUNCE_MS, TrackRows, type TrackRowsLive } from "./TrackRows";
import type { SourceTrack, TrackSel } from "@/lib/types";

/* I4 on the routing page: a live mute and gain per track. They go to the
 * per-track route through `live`, never through `onChange`, because onChange
 * marks the draft dirty and is sent by Save. */
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** A live gain is gathered before it is sent. */
async function debounce() {
  await act(async () => {
    vi.advanceTimersByTime(LIVE_GAIN_DEBOUNCE_MS);
  });
}

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
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const l = live();
    const onChange = renderRows(l);
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "ArrowLeft" }));
    await debounce();
    expect(l.onGain).toHaveBeenCalledWith(0, 0.95);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("the live gain never sends zero: silence is the mute", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const l = live();
    renderRows(l);
    const thumb = screen.getAllByRole("slider")[0];
    await act(async () => fireEvent.keyDown(thumb, { key: "Home" }));
    await debounce();
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

describe("TrackRows live controls keep keyboard focus while a request is in flight", () => {
  it("a pressed Mute stays focusable and ignores a second press until the answer", async () => {
    let finish: () => void = () => {};
    const l = live({ onMute: vi.fn(() => new Promise<void>((r) => (finish = r))) });
    renderRows(l);
    const btn = screen.getByRole("button", { name: "Mute track 1" }) as HTMLButtonElement;
    btn.focus();
    await act(async () => fireEvent.click(btn));
    expect(btn.disabled).toBe(false);
    expect(btn.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(btn);
    await act(async () => fireEvent.click(btn));
    expect(l.onMute).toHaveBeenCalledTimes(1);
    await act(async () => finish());
    expect(btn.getAttribute("aria-disabled")).toBeNull();
  });
});

describe("TrackRows live gain from the keyboard", () => {
  function gated() {
    const finishers: (() => void)[] = [];
    const onGain = vi.fn(() => new Promise<void>((r) => finishers.push(r)));
    return { onGain, answer: async () => act(async () => finishers.shift()!()) };
  }
  const slider = () => screen.getAllByRole("slider")[0];
  const mute = () => screen.getByRole("button", { name: "Mute track 1" }) as HTMLButtonElement;
  const press = (key = "ArrowLeft") => act(async () => fireEvent.keyDown(slider(), { key }));

  // MUTATION: send at once in commitGain (no timer). Observed: FAIL.
  it("a run of key presses sends one request, for where the run ended", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const l = live();
    renderRows(l);
    await press();
    await press();
    await press();
    expect(l.onGain).not.toHaveBeenCalled();
    await debounce();
    expect(l.onGain).toHaveBeenCalledTimes(1);
    expect(l.onGain).toHaveBeenCalledWith(0, 0.85);
  });

  // MUTATION: drop the inFlight check in pump. Observed: FAIL.
  it("keeps one request in flight, then sends only the latest level", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const g = gated();
    renderRows(live({ onGain: g.onGain }));
    await press();
    await debounce();
    expect(g.onGain).toHaveBeenCalledTimes(1);
    await press();
    await debounce();
    await press();
    await debounce();
    // Still one out: the later levels wait for its answer.
    expect(g.onGain).toHaveBeenCalledTimes(1);
    await g.answer();
    expect(g.onGain).toHaveBeenCalledTimes(2);
    expect(g.onGain).toHaveBeenLastCalledWith(0, 0.85);
    await g.answer();
    expect(g.onGain).toHaveBeenCalledTimes(2);
  });

  // MUTATION: clear busy and drag on every answer (the old settle). Observed: FAIL.
  it("Mute stays unavailable, and the thumb stays put, until the last level has answered", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const g = gated();
    const l = live({ onGain: g.onGain });
    renderRows(l);
    await press();
    // Busy from the first key press, before anything is sent.
    expect(mute().getAttribute("aria-disabled")).toBe("true");
    await debounce();
    // A second level is gathered but not yet sent when the first answers.
    await press();
    await g.answer();
    expect(mute().getAttribute("aria-disabled")).toBe("true");
    expect(slider().getAttribute("aria-valuenow")).toBe("0.9");
    await act(async () => fireEvent.click(mute()));
    expect(l.onMute).not.toHaveBeenCalled();
    // Sent, and still busy while it is out.
    await debounce();
    expect(g.onGain).toHaveBeenCalledTimes(2);
    expect(mute().getAttribute("aria-disabled")).toBe("true");
    expect(slider().getAttribute("aria-valuenow")).toBe("0.9");
    await g.answer();
    expect(mute().getAttribute("aria-disabled")).toBeNull();
  });

  // MUTATION: drop the send in the unmount cleanup. Observed: FAIL.
  it("a gain still gathering when the rows unmount is sent, not dropped", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const l = live();
    renderRows(l);
    await press();
    expect(l.onGain).not.toHaveBeenCalled();
    cleanup();
    expect(l.onGain).toHaveBeenCalledWith(0, 0.95);
    await debounce();
    expect(l.onGain).toHaveBeenCalledTimes(1);
  });

  it("a gain committed while a mute is out waits for the mute's answer", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    let finish: () => void = () => {};
    const l = live({ onMute: vi.fn(() => new Promise<void>((r) => (finish = r))) });
    renderRows(l);
    await act(async () => fireEvent.click(mute()));
    await press();
    await debounce();
    expect(l.onGain).not.toHaveBeenCalled();
    await act(async () => finish());
    expect(l.onGain).toHaveBeenCalledWith(0, 0.95);
  });
});
