// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* THE CLIPS PAGE SAYS WHAT IT CAN DO RIGHT NOW (A12, A13, A11).
 *
 * A12: while the buffer is off, the capture card showed a default-styled
 * "Last 30s" button that could not work, and the only way out was a switch two
 * columns away. The card now offers one "Turn on rolling buffer" action in
 * place of the presets, so the operator is never shown a button they cannot
 * press.
 *
 * A13: load() copied the server's window into the input on every 3-second poll,
 * so a typed value reverted before Apply. The poll must not overwrite an edit
 * that has not been saved.
 *
 * A11: the header badge read "Ready" as soon as the buffer was switched on,
 * even while it was still waiting for a stream and the capture buttons were
 * disabled. It reads "Waiting" now, through the kit's status map. */

type Buffer = { enabled: boolean; running: boolean; windowSeconds?: number; buffer: null | { windowSeconds: number; seconds: number; bytes: number; maxBytes: number; bitrateKbps: number; packets: number; datagrams: number; evicted: number; oversized: number; truncated: boolean; videoFound: boolean }; dir: string };

let serverBuffer: Buffer = { enabled: false, running: false, buffer: null, dir: "/x" };

vi.mock("@/lib/autoApi", () => ({
  autoApi: {
    listClips: () =>
      Promise.resolve({
        clips: [],
        buffer: serverBuffer,
        usage: { count: 0, usedBytes: 0, maxBytes: 0, maxClips: 0 },
        bounds: { minWindowSeconds: 5, maxWindowSeconds: 300 },
      }),
    captureClip: () => Promise.resolve({}),
    setClipBuffer: () => Promise.resolve(serverBuffer),
    del: () => Promise.resolve({}),
  },
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ programme: "main", programmeKnown: true }),
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { ClipsPage } = await import("./ClipsPage");

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  serverBuffer = { enabled: false, running: false, buffer: null, dir: "/x" };
});

describe("ClipsPage while the buffer is off (A12)", () => {
  it("offers one turn-on action and no capture preset that cannot work", async () => {
    render(<ClipsPage />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Turn on rolling buffer" })).toBeTruthy(),
    );
    // The disabled "Last 30s" button was the broken-looking primary action.
    expect(screen.queryByRole("button", { name: "Last 30s" })).toBeNull();
  });
});

describe("ClipsPage window input (A13)", () => {
  it("keeps a typed window when the 3-second poll runs before Apply", async () => {
    serverBuffer = {
      enabled: true,
      running: true,
      buffer: { windowSeconds: 60, seconds: 10, bytes: 1, maxBytes: 2, bitrateKbps: 5, packets: 1, datagrams: 1, evicted: 0, oversized: 0, truncated: false, videoFound: true },
      dir: "/x",
    };
    let poll: (() => void) | undefined;
    const realSet = window.setInterval.bind(window);
    vi.spyOn(window, "setInterval").mockImplementation(((fn: () => void, ms?: number) => {
      if (ms === 3000) poll = fn;
      return realSet(() => {}, 1 << 30);
    }) as typeof window.setInterval);

    render(<ClipsPage />);
    const input = (await screen.findByLabelText("Window (seconds)")) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("60"));

    fireEvent.change(input, { target: { value: "45" } });
    await act(async () => {
      poll?.();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(input.value).toBe("45");
  });
});

describe("ClipsPage header status (A11)", () => {
  it("reads Waiting, not Ready, when the buffer is on but no stream is arriving", async () => {
    serverBuffer = { enabled: true, running: false, buffer: null, dir: "/x" };
    render(<ClipsPage />);

    await waitFor(() => expect(screen.getByText("Waiting")).toBeTruthy());
    expect(screen.queryByText("Ready")).toBeNull();
  });
});

/* app-council A14. The switch is stored per programme and survives a restart,
 * so the page no longer apologises that it is "off again after a restart" --
 * that sentence would now be false. It says the opposite, in readable text,
 * and a switched-off buffer shows the window it will come back with rather
 * than a hard-coded 60. */
describe("ClipsPage buffer persistence (app-council A14)", () => {
  it("says the switch is saved and no longer claims it is off after a restart", async () => {
    render(<ClipsPage />);
    await waitFor(() =>
      expect(screen.getByText(/stays on after a restart/)).toBeTruthy(),
    );
    expect(screen.queryByText(/off again after a restart/)).toBeNull();
  });

  it("seeds the window from the stored value while the buffer is off", async () => {
    serverBuffer = { enabled: false, running: false, buffer: null, dir: "/x", windowSeconds: 90 };
    render(<ClipsPage />);
    const input = (await screen.findByLabelText("Window (seconds)")) as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("90"));
  });
});

describe("ClipsPage empty clip list", () => {
  it("uses the kit empty state with a sentence on what will appear", async () => {
    render(<ClipsPage />);
    await waitFor(() => expect(screen.getByText("No clips yet.")).toBeTruthy());
    expect(screen.getByText(/appears here, ready to download/)).toBeTruthy();
  });
});
