// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { PostProdSettings } from "@/lib/types";

/* The scheduling windows are an editable list with no id of their own. Removing
 * a middle window must keep the other windows' time inputs as the same DOM
 * nodes: an index key would hand the last window's input to the window above it,
 * and whatever the operator had typed would move with the wrong row. */

const saved: PostProdSettings[] = [];

const windows = [
  { tz: "Asia/Tokyo", startMinutes: 120, endMinutes: 360, days: [] },
  { tz: "Asia/Tokyo", startMinutes: 480, endMinutes: 600, days: [] },
  { tz: "Asia/Tokyo", startMinutes: 720, endMinutes: 840, days: [] },
];

const policy = {
  enabled: true,
  concurrency: 1,
  defaultMode: "deferred",
  yieldToStream: true,
  cpuCeilingPercent: 85,
  cpuResumePercent: 65,
  cpuSustainedSeconds: 30,
  cpuSettleSeconds: 20,
  avoidGpuWhenStreaming: true,
  gpuBusy: false,
  batteryFloorPercent: 20,
  thermalCeilingC: 90,
  niceLevel: 10,
  idleIo: true,
  ingestLingerSeconds: 30,
  deferSeconds: 30,
  retainDays: 7,
  retainJobs: 50,
  kinds: [{ kind: "transcode", mode: "scheduled", windows }],
};

const overview = {
  available: true,
  paused: false,
  stats: { running: 0, queued: 0, deferred: 0, failed: 0, completed: 0 },
  counts: {},
  policy,
  kinds: [
    {
      kind: "transcode",
      label: "Proxy transcode",
      description: "A smaller copy for scrubbing.",
      mode: "scheduled",
      usesGpu: false,
      ignoreIngest: false,
      overridden: true,
      available: true,
    },
  ],
  active: [],
  recent: [],
  whisper: { available: true, unavailable: "", models: [], defaultModel: "base.en" },
};

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview),
    putJobPolicy: (body: PostProdSettings) => {
      saved.push(body);
      return Promise.resolve({ policy: body, restartRequired: false });
    },
  },
}));

vi.mock("sonner", () => ({
  toast: { error: () => {}, success: () => {}, info: () => {}, warning: () => {} },
}));

const { JobsPage } = await import("./JobsPage");
const { translate } = await import("@/lib/i18n");

const T = (k: Parameters<typeof translate>[1]) => translate("en", k);

afterEach(() => {
  cleanup();
  saved.length = 0;
});

async function openPolicy(): Promise<void> {
  render(<JobsPage />);
  await waitFor(() => expect(screen.getByText(T("jobs.policy"))).toBeTruthy());
  // Radix Tabs switches on mousedown, not on click.
  fireEvent.mouseDown(screen.getByText(T("jobs.policy")));
  await waitFor(() => expect(screen.getAllByLabelText(T("jobs.windowStart")).length).toBe(3));
}

describe("JobsPage: scheduling window rows", () => {
  it("keeps the other windows' inputs when a middle window is removed", async () => {
    await openPolicy();
    const lastStart = screen.getByDisplayValue("12:00");
    const firstStart = screen.getByDisplayValue("02:00");

    const removeButtons = screen.getAllByRole("button", { name: T("jobs.removeWindow") });
    fireEvent.click(removeButtons[1]);

    expect(screen.queryByDisplayValue("08:00")).toBeNull();
    expect(screen.getAllByLabelText(T("jobs.windowStart"))).toHaveLength(2);
    expect(screen.getByDisplayValue("12:00")).toBe(lastStart);
    expect(screen.getByDisplayValue("02:00")).toBe(firstStart);
  });

  it("keeps a window's input while it is being edited", async () => {
    await openPolicy();
    const lastStart = screen.getByDisplayValue("12:00");

    fireEvent.change(screen.getByDisplayValue("08:00"), { target: { value: "09:30" } });

    expect(screen.getByDisplayValue("12:00")).toBe(lastStart);
    expect(screen.getByDisplayValue("09:30")).toBeTruthy();
  });

  it("sends the remaining windows without any row key", async () => {
    await openPolicy();
    const removeButtons = screen.getAllByRole("button", { name: T("jobs.removeWindow") });
    fireEvent.click(removeButtons[1]);

    fireEvent.click(await screen.findByRole("button", { name: "Save policy" }));

    await waitFor(() => expect(saved.length).toBe(1));
    const sent = saved[0].kinds?.[0].windows ?? [];
    expect(sent.map((w) => w.startMinutes)).toEqual([120, 720]);
    for (const w of sent) expect(Object.keys(w).sort()).toEqual(["days", "endMinutes", "startMinutes", "tz"]);
  });
});
