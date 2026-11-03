// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* PUBLISHING IS STARTED FROM A FINISHED CLIP EXPORT AND NOWHERE ELSE ON THIS PAGE.
 *
 * A done clip.export row offers Publish, which opens the dialog (whose own
 * Publish button is the only thing that posts). A publish job's row offers no
 * Retry: the server refuses a generic retry for the kind, because running a
 * stopped publish again is a second publish and needs a second click. */

const overview = {
  available: true,
  paused: false,
  stats: { running: 0, queued: 0, deferred: 0, failed: 1, completed: 1 },
  counts: {},
  policy: { retainDays: 7, retainJobs: 50, kinds: [] },
  kinds: [],
  active: [],
  recent: [
    { id: 7, kind: "clip.export", label: "Clip export", state: "done", progress: 1, attempts: 1, maxAttempts: 3 },
    { id: 9, kind: "clip.publish", label: "Clip publish", state: "failed", progress: 0.5, attempts: 5, maxAttempts: 5, error: "x" },
    { id: 10, kind: "transcode", label: "Proxy transcode", state: "failed", progress: 0, attempts: 3, maxAttempts: 3, error: "y" },
  ],
  whisper: { available: true, unavailable: "" },
};

const publishTargets = vi.fn((_id: number) => new Promise(() => undefined));

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview),
    deleteJob: () => Promise.resolve({}),
    retryJob: () => Promise.resolve({}),
    publishTargets: (id: number) => publishTargets(id),
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {}, warning: () => {} },
}));

const { JobsPage } = await import("./JobsPage");
const { translate } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  publishTargets.mockClear();
});

async function openHistory() {
  render(
    <MemoryRouter>
      <JobsPage />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByText(translate("en", "jobs.history"))).toBeTruthy());
  fireEvent.mouseDown(screen.getByText(translate("en", "jobs.history")));
}

describe("publishing from the jobs page", () => {
  it("offers Publish only on a finished clip export, and it opens the dialog without posting", async () => {
    await openHistory();
    const buttons = await screen.findAllByRole("button", { name: translate("en", "publish.open") });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(publishTargets).toHaveBeenCalledWith(7));
    expect(screen.getByText(translate("en", "publish.title"))).toBeTruthy();
  });

  it("hides Retry on a publish job but keeps it on other failed jobs", async () => {
    await openHistory();
    const retries = await screen.findAllByRole("button", { name: translate("en", "jobs.retry") });
    // Three rows: the done export and the failed transcode keep Retry; the publish job loses it.
    expect(retries).toHaveLength(2);
  });
});
