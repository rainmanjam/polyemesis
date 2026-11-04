// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

/* The header's running badge reads one as one. The catalogue has no plural
 * rules, so a single running job has its own key: in German it read
 * "1 laufen". Harness from JobsPage.purge.test.tsx. */

let running = 0;

const overview = () => ({
  available: true,
  paused: false,
  stats: { running, queued: 0, deferred: 0, failed: 0, completed: 0 },
  counts: {},
  policy: { retainDays: 7, retainJobs: 50, kinds: {} },
  kinds: [],
  active: [],
  recent: [],
  whisper: { available: true, unavailable: "" },
});

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview()),
    purgeJobs: () => Promise.resolve({ purged: 0 }),
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {} },
}));

const { JobsPage } = await import("./JobsPage");
const { setLanguage } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  setLanguage("en");
  running = 0;
});

describe("JobsPage: the running badge reads one as one (de)", () => {
  it("says 1 läuft with one job running", async () => {
    setLanguage("de");
    running = 1;
    render(<JobsPage />);

    expect(await screen.findByText("1 läuft")).toBeTruthy();
    expect(screen.queryByText("1 laufen")).toBeNull();
  });

  it("says 2 laufen with two jobs running", async () => {
    setLanguage("de");
    running = 2;
    render(<JobsPage />);

    expect(await screen.findByText("2 laufen")).toBeTruthy();
  });
});
