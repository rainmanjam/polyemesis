// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

/* THE JOBS PAGE TREATS ITS OWN OPERATOR AS THE PROBLEM. (A59, A60, A61, A62, A64)
 *
 * Four things an operator hits on a bad day, each pinned here:
 *
 *   A59  An unreachable server toasted the operator every four seconds. The
 *        first failure is news; the next thirty are the same news, and a red
 *        toast every poll buries the one error that is new. Now the first
 *        failure toasts once, and a single inline line says the connection is
 *        lost and retrying, until a poll succeeds.
 *
 *   A60  The X on a RUNNING transcode cancelled it on the click, losing the work
 *        done so far. Only a running job asks first; a queued job has no
 *        progress to lose and still cancels directly.
 *
 *   A61  The CPU gate badge said only "42%" when it was over the ceiling, so the
 *        reason a job was held lived in a tooltip. Over the ceiling it now says
 *        so in words.
 *
 *   A62  Clearing a number field fired NaN, which the page turned into the
 *        minimum, so the field snapped to "1" mid-keystroke. The field now keeps
 *        what is typed and commits on blur or Enter.
 *
 *   A64  "Pause all" appeared over an empty queue, where it has no target. It is
 *        now shown only when there is something to pause or the queue is already
 *        paused, so the button that resumes a paused queue is never lost.
 *
 * Plain functions rather than `vi.fn()` for the API, as in the other Jobs tests:
 * a rejecting spy fails its own test. */

const toastErrors: string[] = [];
const cancelled: number[] = [];
let overviewFails = false;

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
  kinds: [],
};

function overview(active: unknown[], paused = false, cpuPercent = 42) {
  return {
    available: true,
    paused,
    stats: { running: active.length, queued: 0, deferred: 0, failed: 0, completed: 0 },
    counts: {},
    policy,
    kinds: [],
    active,
    recent: [],
    governor: {
      at: "2026-10-30T00:00:00Z",
      enabled: true,
      paused,
      verdicts: [],
      gates: {
        at: "2026-10-30T00:00:00Z",
        cpuSustained: false,
        cpuPercent,
        cpuOverCeiling: cpuPercent >= 85,
        gpuBusy: false,
        onBattery: false,
        tooHot: false,
        ingestLive: false,
        power: { known: false, percent: -1 },
      },
      yielding: [],
    },
    whisper: { available: true, unavailable: "", models: [], defaultModel: "base.en" },
  };
}

let current = overview([]);

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () =>
      overviewFails ? Promise.reject(new Error("connection refused")) : Promise.resolve(current),
    cancelJob: (id: number) => {
      cancelled.push(id);
      return Promise.resolve({});
    },
    pauseJobs: () => Promise.resolve({}),
    resumeJobs: () => Promise.resolve({}),
  },
}));

vi.mock("sonner", () => ({
  toast: {
    error: (msg: string) => {
      toastErrors.push(msg);
    },
    success: () => {},
    info: () => {},
    warning: () => {},
  },
}));

const { JobsPage } = await import("./JobsPage");
const { translate } = await import("@/lib/i18n");

afterEach(() => {
  cleanup();
  toastErrors.length = 0;
  cancelled.length = 0;
  overviewFails = false;
  current = overview([]);
});

const runningJob = {
  id: 3,
  kind: "transcode",
  label: "Interview cut",
  state: "running",
  progress: 0.4,
  attempts: 1,
  maxAttempts: 3,
  target: "recording:1",
  priority: "normal",
};

const queuedJob = { ...runningJob, id: 4, label: "Queued cut", state: "queued", progress: 0 };

describe("JobsPage: an unreachable server (A59)", () => {
  it("toasts the first failed poll once, then says in place that the connection is lost", async () => {
    overviewFails = true;
    render(<JobsPage />);
    await waitFor(() => expect(toastErrors.length).toBe(1));

    // The poll runs every 4 seconds; wait past one more tick while it still fails.
    await new Promise((r) => setTimeout(r, 4300));

    expect(toastErrors.length, "a second failed poll must not toast again").toBe(1);
    expect(screen.getByRole("status").textContent).toContain(
      translate("en", "jobs.connectionLostRetrying"),
    );
  }, 15000);

  it("clears the inline line when a poll succeeds again", async () => {
    overviewFails = true;
    render(<JobsPage />);
    await waitFor(() => expect(toastErrors.length).toBe(1));

    overviewFails = false;
    await new Promise((r) => setTimeout(r, 4300));

    expect(screen.queryByText(translate("en", "jobs.connectionLostRetrying"))).toBeNull();
  }, 15000);
});

describe("JobsPage: cancelling work (A60)", () => {
  it("asks before cancelling a RUNNING transcode, and cancels only once confirmed", async () => {
    current = overview([runningJob]);
    render(<JobsPage />);
    const cancel = await screen.findByRole("button", { name: translate("en", "jobs.cancel") });

    fireEvent.click(cancel);

    expect(cancelled, "the click must not cancel a running job by itself").toEqual([]);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(translate("en", "jobs.cancelRunningTitle"))).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("button", { name: translate("en", "jobs.cancelRunningConfirm") }));
    await waitFor(() => expect(cancelled).toEqual([3]));
  });

  it("cancels a QUEUED job straight away, because no progress is lost", async () => {
    current = overview([queuedJob]);
    render(<JobsPage />);
    const cancel = await screen.findByRole("button", { name: translate("en", "jobs.cancel") });

    fireEvent.click(cancel);

    await waitFor(() => expect(cancelled).toEqual([4]));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("JobsPage: the CPU gate says why it holds (A61)", () => {
  it("shows the words CPU over ceiling beside the percentage when the gate is closed", async () => {
    current = overview([], false, 92);
    render(<JobsPage />);

    expect(await screen.findByText(translate("en", "jobs.gateCpuOverCeiling"))).toBeTruthy();
  });

  it("shows no CPU gate words while the load is under the ceiling", async () => {
    current = overview([], false, 42);
    render(<JobsPage />);
    await screen.findByText(translate("en", "jobs.inQueue"));

    expect(screen.queryByText(translate("en", "jobs.gateCpuOverCeiling"))).toBeNull();
  });
});

describe("JobsPage: numeric policy fields while typing (A62)", () => {
  it("keeps an emptied field empty instead of snapping it to the minimum", async () => {
    current = overview([]);
    render(<JobsPage />);
    fireEvent.mouseDown(await screen.findByText(translate("en", "jobs.policy")));
    const ceiling = (await screen.findByLabelText(translate("en", "jobs.cpuCeiling"))) as HTMLInputElement;
    expect(ceiling.value).toBe("85");

    fireEvent.change(ceiling, { target: { value: "" } });

    expect(ceiling.value, "clearing the field must not write the minimum into it").toBe("");
  });
});

describe("JobsPage: Pause all over an empty queue (A64)", () => {
  it("does not offer Pause all when nothing is queued and the queue is running", async () => {
    current = overview([]);
    render(<JobsPage />);
    await screen.findByText(translate("en", "jobs.inQueue"));

    expect(screen.queryByRole("button", { name: translate("en", "jobs.pauseAll") })).toBeNull();
  });

  it("still offers Resume all when the queue is paused, even with nothing queued", async () => {
    current = overview([], true);
    render(<JobsPage />);

    expect(await screen.findByRole("button", { name: translate("en", "jobs.resumeAll") })).toBeTruthy();
  });
});
