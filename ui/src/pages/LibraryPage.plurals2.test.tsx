// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The Library reads one as one. The catalogue has no plural rules, so a count
 * of exactly 1 has its own key: a one-track recording's row said "1 tracks",
 * its player "1 audio tracks in the master", and a Spanish search with one
 * hit "1 mostrados". The Rebuild sessions toast carries three counts, so it
 * was rewritten as "label: value" instead of growing eight keys.
 *
 * Harnesses from LibraryPage.content.test.tsx (the page) and
 * LibraryPage.transcript.test.tsx (PlayerDialog). */

const toasts: string[] = [];

vi.mock("@/components/MediaUploads", () => ({
  MediaUploads: () => <div>media uploads card</div>,
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: (m: string) => toasts.push(m), info: (m: string) => toasts.push(m) },
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { ...actual.api } };
});

import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import { LibraryPage, PlayerDialog } from "./LibraryPage";
import type { LibraryRecording, LibraryView, TranscriptHit } from "@/lib/types";

const rec = (over: Record<string, unknown> = {}) => ({
  id: 7,
  filename: "rec-7.mkv",
  startedAt: "2026-01-01T00:00:00Z",
  bytes: 1000,
  durationMs: 60_000,
  tracks: 1,
  hasTranscript: false,
  assets: { proxy: true, poster: false },
  ...over,
});

const library = (over: Partial<LibraryView> = {}): LibraryView =>
  ({
    sessions: [],
    ungrouped: [],
    tags: [],
    speakers: [],
    jobsAvailable: true,
    transcribeAvailable: true,
    markers: ["", ""],
    ...over,
  }) as unknown as LibraryView;

function draw(view: LibraryView) {
  api.library = vi.fn().mockResolvedValue(view);
  return render(
    <MemoryRouter>
      <LibraryPage />
    </MemoryRouter>,
  );
}

const hit = (i: number): TranscriptHit => ({
  segmentId: i,
  recordingId: 7,
  recording: "rec-7.mkv",
  track: 0,
  startMs: i * 1000,
  endMs: i * 1000 + 900,
  at: "2026-01-01T00:00:00Z",
  text: `line ${i}`,
  snippet: `line ${i}`,
  score: 1,
});

function drawPlayer(tracks: number) {
  api.libraryRecording = vi.fn().mockResolvedValue({
    recording: {
      id: 1,
      filename: "rec-1.mkv",
      startedAt: "2026-01-01T00:00:00Z",
      finishedAt: "2026-01-01T00:10:00Z",
      bytes: 1000,
      durationMs: 600_000,
      tracks,
      hasTranscript: false,
      assets: { proxy: false, poster: false },
    } as unknown as LibraryRecording,
    transcriptTracks: null,
  });
  api.transcript = vi.fn().mockResolvedValue({ transcript: null, merged: [], speakers: [], segments: 0 });
  return render(
    <PlayerDialog target={{ recordingId: 1 }} onClose={() => {}} jobsAvailable transcribeAvailable onChanged={() => {}} />,
  );
}

afterEach(() => {
  cleanup();
  setLanguage("en");
  toasts.length = 0;
  vi.restoreAllMocks();
});

describe("Library search: hits shown when the total is unknown, in Spanish", () => {
  async function search(n: number) {
    setLanguage("es");
    api.searchTranscripts = vi.fn().mockResolvedValue({
      hits: Array.from({ length: n }, (_, i) => hit(i + 1)),
      total: -1,
      limit: 40,
      offset: 0,
      markers: ["[[", "]]"],
    });
    draw(library());
    const box = await screen.findByRole("textbox", { name: "Buscar en las transcripciones" });
    fireEvent.change(box, { target: { value: "line" } });
  }

  it("says 1 mostrado for one hit", async () => {
    await search(1);
    expect(await screen.findByText("1 mostrado")).toBeTruthy();
    expect(screen.queryByText("1 mostrados")).toBeNull();
  });

  it("says 2 mostrados for two hits", async () => {
    await search(2);
    expect(await screen.findByText("2 mostrados")).toBeTruthy();
  });
});

describe("Library row: the recording's track count", () => {
  it("says · 1 track for a one-track recording", async () => {
    draw(library({ ungrouped: [rec({ tracks: 1 })] as never }));
    expect(await screen.findByText(/ · 1 track$/)).toBeTruthy();
    expect(screen.queryByText(/1 tracks/)).toBeNull();
  });

  it("says · 2 tracks for a two-track recording", async () => {
    draw(library({ ungrouped: [rec({ tracks: 2 })] as never }));
    expect(await screen.findByText(/ · 2 tracks$/)).toBeTruthy();
  });
});

describe("PlayerDialog: audio tracks in the master", () => {
  it("says 1 audio track in the master", async () => {
    drawPlayer(1);
    expect(await screen.findByText("1 audio track in the master")).toBeTruthy();
    expect(screen.queryByText("1 audio tracks in the master")).toBeNull();
  });

  it("says 2 audio tracks in the master", async () => {
    drawPlayer(2);
    expect(await screen.findByText("2 audio tracks in the master")).toBeTruthy();
  });
});

describe("Library Rebuild sessions: the toast", () => {
  async function regroup(n: number) {
    api.regroupSessions = vi.fn().mockResolvedValue({ created: n, assigned: n, extended: n });
    draw(library());
    fireEvent.click(await screen.findByRole("button", { name: /Rebuild sessions/ }));
    await waitFor(() => expect(toasts.length).toBeGreaterThan(0));
  }

  it("says Regrouped — new: 1 · segments assigned: 1 · extended: 1.", async () => {
    await regroup(1);
    expect(toasts).toContain("Regrouped — new: 1 · segments assigned: 1 · extended: 1.");
    expect(toasts).not.toContain("Regrouped: 1 new, 1 segments assigned, 1 extended.");
  });

  it("says Regrouped — new: 2 · segments assigned: 2 · extended: 2.", async () => {
    await regroup(2);
    expect(toasts).toContain("Regrouped — new: 2 · segments assigned: 2 · extended: 2.");
  });
});
