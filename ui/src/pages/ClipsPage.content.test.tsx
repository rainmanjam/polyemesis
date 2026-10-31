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

type Buffer = { enabled: boolean; running: boolean; buffer: null | { windowSeconds: number; seconds: number; bytes: number; maxBytes: number; bitrateKbps: number; packets: number; datagrams: number; evicted: number; oversized: number; truncated: boolean; videoFound: boolean }; dir: string };

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
