// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

/* The editor's video carries the recording's caption file as a <track> when the
 * recording is a single segment.
 *
 * (Scaffold copied from the plurals test:) The clip editor's transcript card reads one as one. The catalogue has no
 * plural rules, so a transcript of exactly one line has its own key: its
 * badge said "1 lines".
 *
 * The editor is heavy, so everything beside the transcript card is stubbed:
 * the timeline (it measures itself with a ResizeObserver jsdom lacks), the
 * highlight, vertical-export and publish panels, and every request but the
 * three the card needs. */

let lines: unknown[] = [];


const source = {
  recordingId: 5,
  recording: "rec-5.mkv",
  startedAt: "2026-01-01T00:00:00Z",
  durationMs: 60_000,
  segments: [{ recordingId: 5, spriteVtt: "/sprites/a.vtt", mediaBase: "/m/", startMs: 0, endMs: 60000, proxy: "/m/proxy.mp4" }],
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

describe("ClipEditor caption track", () => {
  it("attaches the recording's caption file to a single-segment proxy", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    const { container } = render(
      <MemoryRouter initialEntries={["/clips/5"]}>
        <Routes>
          <Route path="/clips/:id" element={<ClipEditor />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(container.querySelector("video track")).not.toBeNull());
    expect(container.querySelector("video track")?.getAttribute("src")).toBe(
      "/api/v1/library/recordings/5/transcript/export?format=vtt",
    );
    vi.unstubAllGlobals();
  });
});
