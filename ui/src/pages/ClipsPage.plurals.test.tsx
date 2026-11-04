// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* The retention sentence reads one as one. The catalogue has no plural rules,
 * so count 1 has its own key: a one-clip limit read "at most 1 clips". */

let maxClips = 0;

vi.mock("@/lib/autoApi", () => ({
  autoApi: {
    listClips: () =>
      Promise.resolve({
        clips: [],
        buffer: { enabled: false, running: false, buffer: null, dir: "/x" },
        usage: { count: 0, usedBytes: 0, maxBytes: 0, maxClips },
        bounds: { minWindowSeconds: 5, maxWindowSeconds: 300 },
      }),
    captureClip: () => Promise.resolve({}),
    setClipBuffer: () => Promise.resolve({}),
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
  maxClips = 0;
});

describe("ClipsPage: the retention limit reads one as one", () => {
  it("says at most 1 clip", async () => {
    maxClips = 1;
    render(<ClipsPage />);
    expect(await screen.findByText(/^Retention keeps at most 1 clip and 0 B;/)).toBeTruthy();
    expect(screen.queryByText(/at most 1 clips/)).toBeNull();
  });

  it("says at most 2 clips", async () => {
    maxClips = 2;
    render(<ClipsPage />);
    expect(await screen.findByText(/^Retention keeps at most 2 clips and 0 B;/)).toBeTruthy();
  });
});
