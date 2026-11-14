// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* Three kinds of row, and each must say what is true of it:
 *  - measured, aligned: its length, no badge;
 *  - measured, rough: its length and the warning "Rough start";
 *  - not measured (no record: captured before the server kept one): a dash and
 *    the neutral "Start not checked" -- not a length, not "Rough start", and not
 *    nothing, which would read as "this starts clean". */

const clip = (name: string, seconds: number, aligned: boolean, measured: boolean) => ({
  name,
  bytes: 1000,
  seconds,
  startedAt: "2026-11-14T10:00:00Z",
  createdAt: "2026-11-14T10:00:10Z",
  keyframeAligned: aligned,
  measured,
});

vi.mock("@/lib/autoApi", () => ({
  autoApi: {
    listClips: () =>
      Promise.resolve({
        clips: [
          clip("clip-a.ts", 12.5, true, true),
          clip("clip-b.ts", 0, false, false),
          clip("clip-c.ts", 3, false, true),
        ],
        buffer: { enabled: false, running: false, buffer: null, dir: "/x" },
        usage: { count: 3, usedBytes: 3000, maxBytes: 0, maxClips: 0 },
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

const rowOf = (name: string) => screen.getByText(name).closest("tr")!;

describe("ClipsPage rows", () => {
  it("shows a measured clip's length, and a warning only for a measured rough start", async () => {
    render(<ClipsPage />);
    await waitFor(() => expect(screen.getByText("clip-a.ts")).toBeTruthy());
    expect(rowOf("clip-a.ts").textContent).toContain("12.5s");
    expect(rowOf("clip-a.ts").textContent).not.toMatch(/rough start|start not checked/i);
    expect(rowOf("clip-c.ts").textContent).toContain("3.0s");
    expect(rowOf("clip-c.ts").textContent).toMatch(/rough start/i);
  });

  it("says 'Start not checked' with a dash for a clip that was never measured", async () => {
    render(<ClipsPage />);
    await waitFor(() => expect(screen.getByText("clip-b.ts")).toBeTruthy());
    const row = rowOf("clip-b.ts");
    expect(row.textContent).toMatch(/start not checked/i);
    expect(row.textContent).not.toMatch(/rough start/i);
    expect(row.textContent).toContain("—");
    expect(row.textContent).not.toContain("0.0s");
    // Neutral, not the warning style.
    const badge = screen.getByText(/start not checked/i);
    expect(badge.className).not.toContain("warn");
  });
});
