// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* TWO POSITIVE CLAIMS THIS PAGE WAS NOT ENTITLED TO MAKE.
 *
 *   1. `recordings` was a plain array, and a failed Promise.all left it `[]`
 *      with `loading` false -- so a page that could not read the index drew
 *      "No recordings yet. Enable recording on the right and start a stream."
 *      over a disk that may be full of them.
 *   2. The stems GET was caught to `[]` so that losing it would not cost the
 *      recordings list. That half is right. What it also did was make EVERY
 *      row claim it has no per-track files: an em dash in the Stems column and
 *      no disclosure chevron, which is exactly how a row with stems looks
 *      when it genuinely has none.
 *
 * Both are lib/readState.ts's stated bug: a failed read STORED AS THE SAME
 * VALUE as a successful empty one.
 *
 * Plain functions rather than `vi.fn()` -- see ClipsPage.readState.test.tsx
 * for why a rejecting spy fails its own test. */

const REC = {
  id: 1,
  filename: "rec-20260727.mkv",
  startedAt: "2026-07-27T10:00:00Z",
  durationMs: 60_000,
  bytes: 1024,
  tracks: 2,
};

let recordings: () => Promise<unknown> = () => Promise.resolve([]);
let stems: () => Promise<unknown> = () => Promise.resolve([]);

vi.mock("@/lib/api", () => ({
  api: {
    listRecordings: () => recordings(),
    recordingUsage: () =>
      Promise.resolve({
        bytes: 0,
        files: 0,
        storage: { halted: false, reason: null },
      }),
    getSettings: () => Promise.resolve({ recording: {}, postProd: {} }),
    putSettings: (s: unknown) => Promise.resolve(s),
    deleteRecording: () => Promise.resolve({}),
    downloadUrl: (id: number) => `/dl/${id}`,
  },
}));

vi.mock("@/lib/autoApi", () => ({
  autoApi: { get: () => stems() },
}));

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({ recordingsRevision: 0, status: null }),
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { RecordingsPage } = await import("./RecordingsPage");
const { translate } = await import("@/lib/i18n");

afterEach(cleanup);

describe("RecordingsPage and a read that failed", () => {
  it("does not claim the disk is empty when the index could not be read", async () => {
    recordings = () => Promise.reject(new Error("boom"));
    stems = () => Promise.resolve([]);
    render(<RecordingsPage />);

    await waitFor(() =>
      expect(screen.getByText(translate("en", "rec.listUnread"))).toBeTruthy(),
    );
    expect(
      screen.queryByText(translate("en", "rec.empty")),
      "a failed read rendered the empty state, which asserts the server answered",
    ).toBeNull();
  });

  it("still says so when the server really did answer with nothing", async () => {
    recordings = () => Promise.resolve([]);
    stems = () => Promise.resolve([]);
    render(<RecordingsPage />);

    await waitFor(() =>
      expect(screen.getByText(translate("en", "rec.empty"))).toBeTruthy(),
    );
    expect(screen.queryByText(translate("en", "rec.listUnread"))).toBeNull();
  });

  /* The stems half. The recordings list still arrives -- that part of the
   * original catch was deliberate and is kept -- but no row may now say it has
   * no per-track files, because nobody asked and got an answer. */
  it("does not claim a segment has no stems when the stems read failed", async () => {
    recordings = () => Promise.resolve([REC]);
    stems = () => Promise.reject(new Error("no stems endpoint"));
    render(<RecordingsPage />);

    await waitFor(() => expect(screen.getByText(REC.filename)).toBeTruthy());
    expect(
      screen.getByTitle(translate("en", "rec.stemsUnread")),
      "the Stems column drew an em dash for a read that never came back, which " +
        "is byte-for-byte how a segment with no stems is drawn",
    ).toBeTruthy();
  });

  it("draws the dash when the stems read did come back empty", async () => {
    recordings = () => Promise.resolve([REC]);
    stems = () => Promise.resolve([]);
    render(<RecordingsPage />);

    await waitFor(() => expect(screen.getByText(REC.filename)).toBeTruthy());
    expect(screen.queryByTitle(translate("en", "rec.stemsUnread"))).toBeNull();
  });
});
