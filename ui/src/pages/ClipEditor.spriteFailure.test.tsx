// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

/* THE SPRITE-THUMBNAIL MERGE CAN NOT REJECT UNHANDLED. A segment whose sprite
 * VTT cannot be fetched leaves the scrubber plain and raises nothing.
 *
 * (Original note, from the plurals test this scaffold is copied from:) The clip editor's transcript card reads one as one. The catalogue has no
 * plural rules, so a transcript of exactly one line has its own key: its
 * badge said "1 lines".
 *
 * The editor is heavy, so everything beside the transcript card is stubbed:
 * the timeline (it measures itself with a ResizeObserver jsdom lacks), the
 * highlight, vertical-export and publish panels, and every request but the
 * three the card needs. */

let lines: unknown[] = [];

const transcriptLine = (i: number) => ({
  recordingId: 5,
  track: 0,
  startMs: i * 1000,
  endMs: i * 1000 + 900,
  text: `line ${i}`,
});

const source = {
  recordingId: 5,
  recording: "rec-5.mkv",
  startedAt: "2026-01-01T00:00:00Z",
  durationMs: 60_000,
  segments: [{ recordingId: 5, spriteVtt: "/sprites/a.vtt", mediaBase: "/m/", startMs: 0, endMs: 60000 }],
  tracks: [],
  hasTranscript: true,
  maxClipSeconds: 600,
  modes: ["fast"],
};

const never = () => new Promise(() => {});

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  http: {
    get: (path: string) => {
      if (path === "/clipper/recordings/5") return Promise.resolve(source);
      if (path === "/clipper/recordings/5/transcript")
        return Promise.resolve({ segments: lines, speakers: null, tracks: null });
      if (path.startsWith("/jobs")) return Promise.resolve({ jobs: [] });
      if (path === "/clipper/templates") return Promise.resolve({});
      return never();
    },
    post: () => never(),
  },
}));

vi.mock("@/components/signature/Timeline", () => ({ Timeline: () => null }));
vi.mock("@/components/HighlightsPanel", () => ({ HighlightsPanel: () => null }));
vi.mock("@/components/VerticalExportPanel", () => ({ VerticalExportPanel: () => null }));
vi.mock("@/components/PublishClipDialog", () => ({ PublishClipDialog: () => null }));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { ClipEditor } = await import("./ClipEditor");
const { setLanguage } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  setLanguage("en");
  lines = [];
});

const unhandled: unknown[] = [];
const onUnhandled = (r: unknown) => unhandled.push(r);

describe("ClipEditor sprite load", () => {
  it("survives a sprite VTT that fails to fetch", async () => {
    process.on("unhandledRejection", onUnhandled);
    const fetchSpy = vi.fn().mockRejectedValue(new Error("offline"));
    vi.stubGlobal("fetch", fetchSpy);
    render(
      <MemoryRouter initialEntries={["/clips/5"]}>
        <Routes>
          <Route path="/clips/:id" element={<ClipEditor />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(fetchSpy).toHaveBeenCalledWith("/sprites/a.vtt", expect.anything()));
    await new Promise((r) => setTimeout(r, 20));
    process.off("unhandledRejection", onUnhandled);
    vi.unstubAllGlobals();
    expect(unhandled).toEqual([]);
  });
});
