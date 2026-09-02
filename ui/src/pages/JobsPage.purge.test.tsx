// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* "PURGE HISTORY" DELETED FILES, AND SAID HOW MANY AFTERWARDS.
 *
 * The button reads as tidying a list. internal/api/jobs.go calls
 * removeClipExport for every job it drops, so it reaches past the history rows
 * and takes the exported clip files off disk with them. The count arrived in a
 * toast -- a report, not a decision: by the time it is on screen the files are
 * gone, and there is no bin they went to.
 *
 * Every other destructive action on this install asks first. This one went out
 * on the click.
 *
 * Plain functions rather than `vi.fn()` for the API -- see
 * ClipsPage.readState.test.tsx for why a rejecting spy fails its own test. */

let purged = 0;
const purgeCalls: unknown[] = [];

const overview = {
  available: true,
  paused: false,
  stats: { running: 0, queued: 0, deferred: 0, failed: 0, completed: 2 },
  counts: {},
  policy: { retainDays: 7, retainJobs: 50, kinds: {} },
  kinds: [],
  active: [],
  recent: [
    { id: 1, kind: "transcode", state: "completed", progress: 1 },
    { id: 2, kind: "transcribe", state: "completed", progress: 1 },
  ],
  whisper: { available: true, unavailable: "" },
};

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview),
    purgeJobs: (body: unknown) => {
      purgeCalls.push(body);
      return Promise.resolve({ purged });
    },
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { JobsPage } = await import("./JobsPage");
const { translate } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  purgeCalls.length = 0;
});

const PURGE = translate("en", "jobs.purge");

async function openHistory() {
  render(<JobsPage />);
  await waitFor(() => expect(screen.getByText(translate("en", "jobs.history"))).toBeTruthy());
  // Radix Tabs switches on mousedown, not on click.
  fireEvent.mouseDown(screen.getByText(translate("en", "jobs.history")));
  await waitFor(() => expect(screen.getByRole("button", { name: PURGE })).toBeTruthy());
}

describe("JobsPage: purging the finished history", () => {
  it("deletes nothing on the click itself", async () => {
    await openHistory();
    fireEvent.click(screen.getByRole("button", { name: PURGE }));

    expect(
      purgeCalls,
      "the purge went out on the first click, taking exported clip files off disk",
    ).toEqual([]);
  });

  it("asks first, and says the files leave the disk", async () => {
    await openHistory();
    fireEvent.click(screen.getByRole("button", { name: PURGE }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain(translate("en", "jobs.purgeDescription"));
    // The pool it is drawn from, as a number.
    expect(dialog.textContent).toContain("2");

    fireEvent.click(screen.getAllByRole("button", { name: PURGE }).at(-1) as HTMLElement);
    await waitFor(() => expect(purgeCalls.length).toBe(1));
  });

  it("leaves the history alone when the dialog is cancelled", async () => {
    await openHistory();
    fireEvent.click(screen.getByRole("button", { name: PURGE }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: translate("en", "common.cancel") }));

    expect(purgeCalls).toEqual([]);
  });
});
