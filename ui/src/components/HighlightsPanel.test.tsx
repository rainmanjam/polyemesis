// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

/* NOTHING IS CLIPPED WITHOUT AN ACCEPT, from the browser's side.
 *
 * The scan button posts a scan and nothing else; Dismiss posts a dismiss; only
 * Accept posts to /accept, and it carries the editor's own export settings with
 * the suggestion's span -- the vertical layout included -- so an accepted
 * suggestion is exactly the clip the editor would have made. */

const calls: { method: string; path: string; body?: unknown }[] = [];

const HIGHLIGHTS = {
  modelAvailable: false,
  modelReason: "the model checker in Automod is switched off",
  suggestions: [
    {
      id: 7, recordingId: 1, startMs: 285_000, endMs: 310_000, score: 0.75, signalScore: 0.75,
      signals: [
        { kind: "chat", startMs: 300_000, endMs: 305_000, strength: 0.5, peak: 40, baseline: 1 },
        { kind: "loudness", startMs: 298_000, endMs: 300_000, strength: 0.5, peak: -16, baseline: -30 },
      ],
      excerpt: "NO WAY he did it", state: "pending", createdAt: "2026-11-02T10:00:00Z",
    },
    {
      id: 8, recordingId: 1, startMs: 400_000, endMs: 420_000, score: 0.4, signalScore: 0.4,
      signals: [], excerpt: "", state: "dismissed", createdAt: "2026-11-02T10:00:00Z",
    },
  ],
};

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  http: {
    get: (path: string) => {
      calls.push({ method: "GET", path });
      if (path.startsWith("/jobs")) return Promise.resolve({ jobs: [] });
      return Promise.resolve(HIGHLIGHTS);
    },
    post: (path: string, body?: unknown) => {
      calls.push({ method: "POST", path, body });
      return Promise.resolve({ id: 99 });
    },
  },
}));

import { HighlightsPanel } from "./HighlightsPanel";

afterEach(() => {
  cleanup();
  calls.length = 0;
});

const posts = () => calls.filter((c) => c.method === "POST");

function renderPanel(onAccepted = () => undefined, onPreview = (_a: number, _b: number) => undefined) {
  return render(
    <HighlightsPanel
      recordingId={1}
      exportBody={() => ({ mode: "precise", container: "mp4", vertical: { layout: "stacked", face: { x: 0.7, y: 0.05, w: 0.25, h: 0.3 } } })}
      onPreview={onPreview}
      onAccepted={onAccepted}
    />,
  );
}

describe("HighlightsPanel", () => {
  it("lists the review queue with the signals that fired and the excerpt", async () => {
    renderPanel();
    const items = await screen.findAllByTestId("highlight");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("NO WAY he did it")).toBeTruthy();
    expect(within(items[0]).getByText(/40/)).toBeTruthy();
    expect(within(items[0]).getByText(/-16/)).toBeTruthy();
    // A decided suggestion offers no buttons.
    expect(within(items[1]).queryAllByRole("button")).toHaveLength(0);
    // The model is unavailable, so its checkbox is disabled and says why.
    expect(screen.getByText(/switched off/)).toBeTruthy();
  });

  it("scans without clipping", async () => {
    renderPanel();
    await screen.findAllByTestId("highlight");
    fireEvent.click(screen.getByRole("button", { name: /find highlights/i }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].path).toBe("/clipper/recordings/1/highlights/scan");
    expect(posts().some((c) => c.path.includes("/accept") || c.path.includes("/export"))).toBe(false);
  });

  it("dismisses without clipping", async () => {
    renderPanel();
    const [first] = await screen.findAllByTestId("highlight");
    fireEvent.click(within(first).getByRole("button", { name: /dismiss/i }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].path).toBe("/clipper/highlights/7/dismiss");
  });

  it("accepts with the editor's settings and the suggestion's span", async () => {
    const accepted = vi.fn();
    renderPanel(accepted);
    const [first] = await screen.findAllByTestId("highlight");
    fireEvent.click(within(first).getByRole("button", { name: /accept/i }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0]).toEqual({
      method: "POST",
      path: "/clipper/highlights/7/accept",
      body: {
        mode: "precise", container: "mp4",
        vertical: { layout: "stacked", face: { x: 0.7, y: 0.05, w: 0.25, h: 0.3 } },
        inMs: 285_000, outMs: 310_000,
      },
    });
    await waitFor(() => expect(accepted).toHaveBeenCalled());
  });

  it("previews a suggestion by moving the editor's in and out", async () => {
    const preview = vi.fn();
    renderPanel(undefined, preview);
    const [first] = await screen.findAllByTestId("highlight");
    fireEvent.click(within(first).getByRole("button", { name: /set in and out/i }));
    expect(preview).toHaveBeenCalledWith(285_000, 310_000);
    expect(posts()).toHaveLength(0);
  });
});
