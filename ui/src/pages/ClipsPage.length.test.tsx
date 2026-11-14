// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* A clip the server has no length for (written before it kept one) must not
 * read "0.0s": that is a measurement, and nobody took it. */

const clip = (name: string, seconds: number, aligned: boolean) => ({
  name,
  bytes: 1000,
  seconds,
  startedAt: "2026-11-14T10:00:00Z",
  createdAt: "2026-11-14T10:00:10Z",
  keyframeAligned: aligned,
});

vi.mock("@/lib/autoApi", () => ({
  autoApi: {
    listClips: () =>
      Promise.resolve({
        clips: [clip("clip-a.ts", 12.5, true), clip("clip-b.ts", 0, true)],
        buffer: { enabled: false, running: false, buffer: null, dir: "/x" },
        usage: { count: 2, usedBytes: 2000, maxBytes: 0, maxClips: 0 },
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
vi.mock("sonner", () => ({ toast: { error: () => {}, success: () => {}, info: () => {} } }));

const { ClipsPage } = await import("./ClipsPage");

afterEach(cleanup);

describe("ClipsPage length column", () => {
  it("shows the measured length, and a dash where there is none", async () => {
    render(<ClipsPage />);
    await waitFor(() => expect(screen.getByText("12.5s")).toBeTruthy());
    expect(screen.queryByText("0.0s")).toBeNull();
    const row = screen.getByText("clip-b.ts").closest("tr")!;
    expect(row.textContent).toContain("—");
    expect(screen.queryByText("Rough start")).toBeNull();
  });
});
