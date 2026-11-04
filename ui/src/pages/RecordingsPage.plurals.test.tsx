// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* The recordings table and usage tiles read one as one. The catalogue has no
 * plural rules, so count 1 has its own key: a one-track recording's badge said
 * "1 audio tracks preserved", and one stem file "Stems (1 files)". */

let recordings: unknown[] = [];
let stems: unknown[] = [];

const recording = (tracks: number) => ({
  id: 1,
  filename: "2026-11-04_12-00-00.mkv",
  startedAt: "2026-11-04T12:00:00Z",
  finishedAt: "2026-11-04T12:30:00Z",
  bytes: 1_000_000,
  durationMs: 1_800_000,
  tracks,
});

const stemFiles = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    name: `2026-11-04_12-00-00.track${i + 1}.flac`,
    master: "2026-11-04_12-00-00",
    track: String(i + 1),
    bytes: 1000,
    startedAt: "2026-11-04T12:00:00Z",
  }));

vi.mock("@/lib/api", () => ({
  api: {
    listRecordings: () => Promise.resolve(recordings),
    recordingUsage: () => Promise.resolve({ bytes: 0, files: 0, storage: { halted: false, reason: null } }),
    getSettings: () =>
      Promise.resolve({
        recording: { enabled: true, segmentSeconds: 30, maxGb: 50, maxAgeHours: 168, minFreeGb: 5, stems: true, stemCodec: "flac" },
        postProd: {},
      }),
    putSettings: (s: unknown) => Promise.resolve(s),
    deleteRecording: () => Promise.resolve({}),
    downloadUrl: (id: number) => `/dl/${id}`,
  },
}));

vi.mock("@/lib/autoApi", () => ({
  autoApi: { get: () => Promise.resolve(stems) },
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ recordingsRevision: 0, status: null }),
  useIngestLive: () => false,
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { RecordingsPage } = await import("./RecordingsPage");

afterEach(() => {
  cleanup();
  recordings = [];
  stems = [];
});

function draw(n: number) {
  recordings = [recording(n)];
  stems = stemFiles(n);
  render(<RecordingsPage />);
}

describe("RecordingsPage: counts read one as one", () => {
  it("titles a one-track recording 1 audio track preserved", async () => {
    draw(1);
    expect(await screen.findByTitle("1 audio track preserved")).toBeTruthy();
    expect(screen.queryByTitle("1 audio tracks preserved")).toBeNull();
  });

  it("titles a two-track recording 2 audio tracks preserved", async () => {
    draw(2);
    expect(await screen.findByTitle("2 audio tracks preserved")).toBeTruthy();
  });

  it("titles one stem file in the singular", async () => {
    draw(1);
    expect(await screen.findByTitle("1 per-track stem file, sample-aligned with the master")).toBeTruthy();
  });

  it("titles two stem files in the plural", async () => {
    draw(2);
    expect(await screen.findByTitle("2 per-track stem files, sample-aligned with the master")).toBeTruthy();
  });

  it("labels the stems tile Stems (1 file)", async () => {
    draw(1);
    expect(await screen.findByText("Stems (1 file)")).toBeTruthy();
    expect(screen.queryByText("Stems (1 files)")).toBeNull();
  });

  it("labels the stems tile Stems (2 files)", async () => {
    draw(2);
    expect(await screen.findByText("Stems (2 files)")).toBeTruthy();
  });
});
