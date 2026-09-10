// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { PostProdSettings } from "@/lib/types";

/* TWO THINGS THE SERVER ACCEPTED AND NO OPERATOR COULD SEND IT.
 *
 * #780: the governor reads four timings out of PostProdSettings -- sustain,
 * settle, linger and defer -- and the policy endpoint has always written them.
 * The CPU ceiling they are measured against sat on this tab with an input; they
 * had none. An operator whose transcodes were suspended by a five-second spike
 * could see the ceiling, could edit the ceiling, and could not reach the window
 * that decided the spike counted, short of hand-editing settings on disk.
 *
 * #784: a scheduling window carries an IANA zone, captured from whichever
 * browser created it, and it was PRINTED. Set a window from a hotel in Tokyo
 * and come home, and 02:00 means something else; the only route to fixing it
 * was deleting the window and rebuilding its days by hand.
 *
 * These are rendered-control tests on purpose. Both fields were in types.ts and
 * on the wire the whole time, so a test that asserts the shape of the payload
 * would have passed against the broken page. What was missing was an input, and
 * what is pinned here is an input: found by its accessible name, typed into,
 * and followed all the way to what Save sends.
 *
 * Plain functions rather than `vi.fn()` for the API -- see
 * ClipsPage.readState.test.tsx for why a rejecting spy fails its own test. */

const saved: PostProdSettings[] = [];

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
  kinds: [
    {
      kind: "transcode",
      mode: "scheduled",
      windows: [{ tz: "Asia/Tokyo", startMinutes: 120, endMinutes: 360, days: [] }],
    },
  ],
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

afterEach(() => {
  cleanup();
  saved.length = 0;
});

/** Renders the page and switches to the Policy tab, where every control under
 *  test lives. */
async function openPolicy(): Promise<void> {
  render(<JobsPage />);
  await waitFor(() => expect(screen.getByText(translate("en", "jobs.policy"))).toBeTruthy());
  // Radix Tabs switches on mousedown, not on click.
  fireEvent.mouseDown(screen.getByText(translate("en", "jobs.policy")));
  await waitFor(() => expect(screen.getByLabelText(translate("en", "jobs.cpuCeiling"))).toBeTruthy());
}

/** The single Save on the tab. It is sticky and appears only once something is
 *  dirty, which is itself deliberate — see the comment above it. */
async function save(): Promise<void> {
  const button = await screen.findByRole("button", { name: "Save policy" });
  fireEvent.click(button);
  await waitFor(() => expect(saved.length).toBe(1));
}

describe("JobsPage: the governor's four timings", () => {
  it("renders an input for each, carrying the saved value", async () => {
    await openPolicy();

    const fields: [string, string][] = [
      ["jobs.cpuSustained", "30"],
      ["jobs.cpuSettle", "20"],
      ["jobs.ingestLinger", "30"],
      ["jobs.deferFor", "30"],
    ];
    for (const [key, want] of fields) {
      const input = screen.getByLabelText(translate("en", key as "jobs.cpuSustained"));
      expect(
        (input as HTMLInputElement).value,
        `${key} has no input showing the policy's value, so the number is unreachable`,
      ).toBe(want);
    }
  });

  it("sends each edited timing to the server", async () => {
    await openPolicy();

    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuSustained")), {
      target: { value: "45" },
    });
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuSettle")), {
      target: { value: "15" },
    });
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.ingestLinger")), {
      target: { value: "60" },
    });
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.deferFor")), {
      target: { value: "90" },
    });
    await save();

    expect(saved[0].cpuSustainedSeconds).toBe(45);
    expect(saved[0].cpuSettleSeconds).toBe(15);
    expect(saved[0].ingestLingerSeconds).toBe(60);
    expect(saved[0].deferSeconds).toBe(90);
  });

  it("says that a zero is the server's own default rather than off", async () => {
    await openPolicy();
    // The three the server substitutes a default for. A field that reads 0 and
    // behaves as thirty seconds is a lie an operator can only catch with a
    // stopwatch, so the hint has to keep saying so.
    for (const key of ["jobs.cpuSustainedHint", "jobs.cpuSettleHint", "jobs.deferHint"] as const) {
      expect(screen.getByText(translate("en", key))).toBeTruthy();
    }
  });
});

describe("JobsPage: a scheduling window's time zone", () => {
  it("is an editable control, not a printed fact", async () => {
    await openPolicy();

    const zone = screen.getByLabelText(
      translate("en", "jobs.windowZone"),
    ) as HTMLInputElement;
    expect(
      zone.value,
      "the window's zone is not in an input, so a window created in the wrong zone can never be corrected",
    ).toBe("Asia/Tokyo");
    expect(zone.readOnly).toBe(false);
    expect(zone.disabled).toBe(false);
  });

  it("restates the window in the zone the operator just chose", async () => {
    await openPolicy();

    fireEvent.change(screen.getByLabelText(translate("en", "jobs.windowZone")), {
      target: { value: "Europe/Berlin" },
    });

    // The summary under the row is the sentence an operator reads back, so it
    // has to move with the field rather than with the value it was created in.
    expect(screen.getByText(/02:00–06:00, every day Europe\/Berlin/)).toBeTruthy();
  });

  it("sends the corrected zone to the server", async () => {
    await openPolicy();

    fireEvent.change(screen.getByLabelText(translate("en", "jobs.windowZone")), {
      target: { value: "Europe/Berlin" },
    });
    await save();

    expect(saved[0].kinds?.[0].windows?.[0].tz).toBe("Europe/Berlin");
    // The rest of the window survives the edit: correcting an hour must not
    // quietly rebuild the days or the times.
    expect(saved[0].kinds?.[0].windows?.[0].startMinutes).toBe(120);
    expect(saved[0].kinds?.[0].windows?.[0].endMinutes).toBe(360);
  });
});
