// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* THE ROW DELETE TOOK THE FILE TOO, AND ASKED NOTHING.
 *
 * internal/api/jobs.go:handleDeleteJob calls removeClipExport once the row is
 * gone, and a clip export's row is the ONLY reference to its file -- the
 * download route is keyed on the job, and the exports directory sits outside
 * the rolling buffer's pruning. So the trash icon in the history table was a
 * file delete wearing a list-tidying icon, and it went out on the click while
 * the bulk Purge button two feet away had asked first since #222.
 *
 * These pin both halves: that nothing is deleted on the click, and that the
 * dialog SAYS WHAT LEAVES -- a confirmation that does not name the loss is a
 * speed bump, not a control. And that it does not claim a loss for a kind that
 * has none, because a warning that cries wolf over a transcode is how the one
 * that matters stops being read.
 *
 * Plain functions rather than `vi.fn()` for the API -- see
 * ClipsPage.readState.test.tsx for why a rejecting spy fails its own test. */

const deleted: number[] = [];

const overview = {
  available: true,
  paused: false,
  stats: { running: 0, queued: 0, deferred: 0, failed: 0, completed: 2 },
  counts: {},
  policy: { retainDays: 7, retainJobs: 50, kinds: [] },
  kinds: [],
  active: [],
  recent: [
    {
      id: 7,
      kind: "clip.export",
      label: "Clip export",
      state: "done",
      progress: 1,
      attempts: 1,
      maxAttempts: 3,
    },
    {
      id: 8,
      kind: "transcode",
      label: "Proxy transcode",
      state: "done",
      progress: 1,
      attempts: 1,
      maxAttempts: 3,
    },
  ],
  whisper: { available: true, unavailable: "" },
};

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview),
    deleteJob: (id: number) => {
      deleted.push(id);
      return Promise.resolve({});
    },
    retryJob: () => Promise.resolve({}),
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {}, warning: () => {} },
}));

const { JobsPage } = await import("./JobsPage");
const { translate } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  deleted.length = 0;
});

const REMOVE = translate("en", "jobs.removeFromHistory");
const CONFIRM = translate("en", "jobs.deleteConfirm");

/** Opens History and returns the row delete buttons, in `recent` order: the
 *  clip export first, the transcode second. */
async function historyDeletes(): Promise<HTMLElement[]> {
  render(<JobsPage />);
  await waitFor(() => expect(screen.getByText(translate("en", "jobs.history"))).toBeTruthy());
  // Radix Tabs switches on mousedown, not on click.
  fireEvent.mouseDown(screen.getByText(translate("en", "jobs.history")));
  await waitFor(() => expect(screen.getAllByRole("button", { name: REMOVE }).length).toBe(2));
  return screen.getAllByRole("button", { name: REMOVE });
}

describe("JobsPage: deleting one row of the finished history", () => {
  it("deletes nothing on the click itself", async () => {
    const rows = await historyDeletes();
    fireEvent.click(rows[0]);

    expect(
      deleted,
      "the delete went out on the first click, taking the exported clip file off disk",
    ).toEqual([]);
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });

  it("names the file loss for the one kind that has a file", async () => {
    const rows = await historyDeletes();
    fireEvent.click(rows[0]);

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain(translate("en", "jobs.deleteTitle"));
    expect(
      dialog.textContent,
      "the dialog does not say the clip file leaves the disk, which is the whole reason it exists",
    ).toContain(translate("en", "jobs.deleteExportDescription"));
  });

  it("claims no file loss for a kind that has none", async () => {
    const rows = await historyDeletes();
    fireEvent.click(rows[1]);

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain(translate("en", "jobs.deleteDescription"));
    expect(
      dialog.textContent,
      "a transcode row deletes no file, and a dialog that says one does teaches the operator to skip the sentence",
    ).not.toContain(translate("en", "jobs.deleteExportDescription"));
  });

  it("deletes the row the operator clicked once they confirm", async () => {
    const rows = await historyDeletes();
    fireEvent.click(rows[0]);
    await screen.findByRole("dialog");

    fireEvent.click(screen.getByRole("button", { name: CONFIRM }));
    await waitFor(() => expect(deleted).toEqual([7]));
  });

  it("leaves the row alone when the dialog is cancelled", async () => {
    const rows = await historyDeletes();
    fireEvent.click(rows[0]);
    await screen.findByRole("dialog");

    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.cancel") }));
    expect(deleted).toEqual([]);
  });
});
