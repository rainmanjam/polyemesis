// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { PostProdSettings } from "@/lib/types";
import defaults from "@/lib/contract/job-policy-defaults.json";

/* THE POLICY TAB LEADS WITH FOUR DECISIONS AND FOLDS THE REST (A63).
 *
 * Sixteen controls sat in one long column, and the four an operator actually
 * comes here to change -- default mode, jobs at once, the CPU ceiling and where
 * it releases -- had the same weight as the ionice class. The tab now leads
 * with those four and a sentence that says what they add up to, and puts the
 * other twelve behind an Advanced disclosure.
 *
 * The disclosure has one rule these tests exist to hold: IT NEVER HIDES A
 * CHANGED OR BROKEN VALUE. It is closed only while every setting inside it is
 * at the server's default and in range; anything else opens it without a
 * click, and keeps it open. A disclosure that folded away a battery floor of 0
 * would show a page that looks like the factory policy and behaves otherwise.
 *
 * jsdom keeps a closed <details>' content in the DOM, so finding a field by its
 * label proves nothing about whether an operator can see it. Every assertion
 * here reads `details.open`. */

const saved: PostProdSettings[] = [];

/** The factory policy, from the same file the page compares against (and that
 *  internal/db's contract test pins to DefaultSettings()). */
const factory = (): PostProdSettings => ({ ...(defaults as PostProdSettings), kinds: [] });

let policy: PostProdSettings = factory();

const overview = () => ({
  available: true,
  paused: false,
  stats: { running: 0, queued: 0, deferred: 0, failed: 0, completed: 0 },
  counts: {},
  policy,
  kinds: [],
  active: [],
  recent: [],
  whisper: { available: true, unavailable: "", models: [], defaultModel: "base.en" },
});

vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    jobsOverview: () => Promise.resolve(overview()),
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
  policy = factory();
});

async function openPolicy(): Promise<void> {
  render(<JobsPage />);
  await waitFor(() => expect(screen.getByText(translate("en", "jobs.policy"))).toBeTruthy());
  // Radix Tabs switches on mousedown, not on click.
  fireEvent.mouseDown(screen.getByText(translate("en", "jobs.policy")));
  await waitFor(() => expect(screen.getByLabelText(translate("en", "jobs.cpuCeiling"))).toBeTruthy());
}

/** The Advanced disclosure, found through a field that lives only inside it. */
function advanced(): HTMLDetailsElement {
  const field = screen.getByLabelText(translate("en", "jobs.niceLevel"));
  const details = field.closest("details");
  expect(details, "the nice level is not inside a <details> disclosure").toBeTruthy();
  return details as HTMLDetailsElement;
}

/** The sentence over the four fields, as one string: it is built from nodes. */
function summary(): string {
  return screen.getByTestId("jobs-policy-summary").textContent ?? "";
}

describe("JobsPage policy: the four decisions up front", () => {
  it("puts default mode, jobs at once, CPU ceiling and CPU resume outside the disclosure", async () => {
    await openPolicy();
    const d = advanced();
    for (const label of [
      translate("en", "jobs.defaultMode"),
      translate("en", "jobsPolicyUx.jobsAtOnce"),
      translate("en", "jobs.cpuCeiling"),
      translate("en", "jobs.cpuResume"),
    ]) {
      const field = screen.getByLabelText(label);
      expect(d.contains(field), `${label} is folded into Advanced`).toBe(false);
    }
  });
});

describe("JobsPage policy: the Advanced disclosure", () => {
  it("is collapsed when every advanced setting is at its default", async () => {
    await openPolicy();
    expect(advanced().open).toBe(false);
  });

  it("opens by itself when a stored advanced value differs from its default", async () => {
    policy = { ...factory(), retainDays: 7 };
    await openPolicy();
    expect(advanced().open, "a changed retention period was folded out of sight").toBe(true);
  });

  it("treats a zero linger as a change, because 0 there means release at once", async () => {
    policy = { ...factory(), ingestLingerSeconds: 0 };
    await openPolicy();
    expect(advanced().open).toBe(true);
  });

  it("treats a zero sustain window as the default, because the governor substitutes it", async () => {
    policy = { ...factory(), cpuSustainedSeconds: 0 };
    await openPolicy();
    expect(advanced().open).toBe(false);
  });

  it("opens by itself when a stored advanced value is out of range", async () => {
    policy = { ...factory(), cpuSustainedSeconds: 5000 };
    await openPolicy();
    expect(advanced().open, "an out-of-range value was folded out of sight").toBe(true);
  });

  it("opens by itself when a switch inside it is off its default", async () => {
    policy = { ...factory(), gpuBusy: true };
    await openPolicy();
    expect(advanced().open).toBe(true);
  });

  it("will not close over a changed value", async () => {
    policy = { ...factory(), batteryFloorPercent: 0 };
    await openPolicy();
    const d = advanced();
    fireEvent.click(d.querySelector("summary")!);
    expect(d.open, "a click on the summary hid a changed value").toBe(true);
  });

  it("stays open when an edit brings the changed value back to its default", async () => {
    // Folding the section shut the moment the last changed field is put back
    // would move the field under the operator's cursor mid-edit.
    policy = { ...factory(), retainDays: 7 };
    await openPolicy();
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.keepForDays")), {
      target: { value: "30" },
    });
    expect(advanced().open).toBe(true);
  });

  it("opens when an advanced value is typed out of range", async () => {
    await openPolicy();
    const d = advanced();
    fireEvent.click(d.querySelector("summary")!);
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.niceLevel")), {
      target: { value: "50" },
    });
    // Now forced: the summary no longer closes it, and the field says why.
    fireEvent.click(d.querySelector("summary")!);
    expect(d.open).toBe(true);
    expect(screen.getByText(translate("en", "jobsPolicyUx.outOfRange", { min: 0, max: 19 }))).toBeTruthy();
  });

  it("opens and closes on its summary while everything inside is at its default", async () => {
    await openPolicy();
    const d = advanced();
    fireEvent.click(d.querySelector("summary")!);
    expect(d.open).toBe(true);
    fireEvent.click(d.querySelector("summary")!);
    expect(d.open).toBe(false);
  });
});

describe("JobsPage policy: the summary sentence", () => {
  it("states the ceiling and the resume level the draft holds", async () => {
    await openPolicy();
    expect(summary()).toContain("CPU is above 85%");
    expect(summary()).toContain("65%");
  });

  it("follows an edit before it is saved", async () => {
    await openPolicy();
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuCeiling")), {
      target: { value: "70" },
    });
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuResume")), {
      target: { value: "50" },
    });
    expect(summary()).toContain("CPU is above 70%");
    expect(summary()).toContain("50%");
    expect(summary()).not.toContain("85%");
  });

  it("says there is no CPU gate when the ceiling is 0", async () => {
    policy = { ...factory(), cpuCeilingPercent: 0 };
    await openPolicy();
    expect(summary()).toBe(translate("en", "jobsPolicyUx.summaryNoCeiling"));
  });

  it("says the policy is off when it is off, whatever the ceiling", async () => {
    policy = { ...factory(), enabled: false };
    await openPolicy();
    expect(summary()).toBe(translate("en", "jobsPolicyUx.summaryOff"));
  });
});

describe("JobsPage policy: Revert and Save", () => {
  it("keeps both buttons on screen while clean, and enables them on an edit", async () => {
    await openPolicy();
    const saveBtn = screen.getByRole("button", { name: "Save policy" }) as HTMLButtonElement;
    const revertBtn = screen.getByRole("button", { name: "Revert" }) as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(true);
    expect(revertBtn.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuCeiling")), {
      target: { value: "70" },
    });
    expect(saveBtn.disabled).toBe(false);
    expect(revertBtn.disabled).toBe(false);
    expect(screen.getByText(translate("en", "save.dirty"))).toBeTruthy();
  });

  it("saves the draft and says Saved", async () => {
    await openPolicy();
    fireEvent.change(screen.getByLabelText(translate("en", "jobs.cpuCeiling")), {
      target: { value: "70" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(saved.length).toBe(1));
    expect(saved[0].cpuCeilingPercent).toBe(70);
    await waitFor(() => expect(screen.getByText(translate("en", "save.saved"))).toBeTruthy());
  });

  it("reverts the draft to the stored policy", async () => {
    await openPolicy();
    const ceiling = screen.getByLabelText(translate("en", "jobs.cpuCeiling")) as HTMLInputElement;
    fireEvent.change(ceiling, { target: { value: "70" } });
    fireEvent.click(screen.getByRole("button", { name: "Revert" }));
    expect(ceiling.value).toBe("85");
    expect(summary()).toContain("CPU is above 85%");
  });
});
