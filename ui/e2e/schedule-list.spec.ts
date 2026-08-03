import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   Browser end-to-end for the schedule list on the Automation page.

   This case exists because of a bug that shipped and was caught in review, not
   because a list was worth a test on principle. `playlist.start` and
   `playlist.stop` carry NO destinations -- the server refuses a playlist
   schedule that names any -- and the list rendered every one of them with
   "every destination" underneath, which is the one thing that validation
   exists to forbid. It also gave `playlist.start` the stopped badge, because
   the badge compared against the bare string "start".

   Neither is reachable by tsc: both compile, and both are wrong only once
   something renders. The Go drift guard
   (internal/scheduler/action_drift_test.go) proves the action REACHES the
   dropdown; it cannot say a word about what the row then says about it.

   The install is shared and mutated in order with the rest of the suite (see
   e2e/playwright.config.ts), so this creates its own schedules and deletes
   them again -- a stray row would change what every later spec sees on
   /automation.
   =========================================================================== */

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

/** Opens /automation and switches to the schedule list.
 *
 *  The page lands on Alerts, and the schedules table is not merely hidden
 *  behind that tab -- it is not in the document at all until the tab is
 *  selected. A test that only navigated found no rows and could not tell that
 *  apart from a row that failed to render, which is how the first version of
 *  this spec failed for a reason that had nothing to do with what it asserts. */
async function openSchedules(page: Page) {
  await page.goto("/automation");
  await page.getByRole("tab", { name: "Schedules" }).click();
  // The schedule table's own header, not `getByRole("table")`: the panel also
  // carries a recent-runs table, and matching both would fail on strict mode
  // rather than on anything this spec is about.
  await expect(page.getByRole("columnheader", { name: "Schedule" })).toBeVisible();
}

/** POSTs a schedule through the same route the dialog uses and returns its id.
 *  Created through the API rather than by driving the dialog on purpose: the
 *  dialog is what the review found CORRECT, and driving it would make this
 *  test pass or fail for the dialog's reasons rather than the list's. */
async function createSchedule(page: Page, name: string, action: string): Promise<number> {
  return page.evaluate(
    async ({ scheduleName, scheduleAction }) => {
      const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
      const csrf = match ? decodeURIComponent(match[1]) : "";
      const res = await fetch("/api/v1/schedules", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          ...(csrf ? { "X-CSRF-Token": csrf } : {}),
        },
        body: JSON.stringify({
          name: scheduleName,
          enabled: false, // Never let a test schedule actually fire.
          action: scheduleAction,
          kind: "daily",
          destinationIds: [],
          tz: "UTC",
          atMinutes: 3 * 60,
          days: [0, 1, 2, 3, 4, 5, 6],
          runAt: new Date(0).toISOString(),
          graceSeconds: 300,
        }),
      });
      if (!res.ok) throw new Error(`schedule create failed (${res.status})`);
      const body = (await res.json()) as { id: number };
      return body.id;
    },
    { scheduleName: name, scheduleAction: action },
  );
}

/** DELETEs a schedule and returns the status, which the caller asserts on. A
 *  cleanup helper whose failure is invisible leaves the shared install dirty
 *  for every spec after it. */
async function deleteSchedule(page: Page, id: number): Promise<number> {
  return page.evaluate(async (scheduleID) => {
    const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
    const csrf = match ? decodeURIComponent(match[1]) : "";
    const res = await fetch(`/api/v1/schedules/${scheduleID}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: csrf ? { "X-CSRF-Token": csrf } : {},
    });
    return res.status;
  }, id);
}

test.describe("the schedule list says what a schedule acts on", () => {
  test("a playlist schedule is not described as targeting destinations", async ({ page }) => {
    await signIn(page);
    const playlistID = await createSchedule(page, "e2e playlist row", "playlist.start");
    // A destination schedule with no destinations, created alongside it, so the
    // assertions below cannot pass merely because "every destination" is absent
    // from the whole page. It is the wording this feature is supposed to KEEP.
    const destID = await createSchedule(page, "e2e destination row", "start");

    try {
      await openSchedules(page);
      const playlistRow = page.getByRole("row").filter({ hasText: "e2e playlist row" });
      const destRow = page.getByRole("row").filter({ hasText: "e2e destination row" });
      await expect(playlistRow).toBeVisible();

      await expect(playlistRow).toContainText("the failover playlist");
      await expect(playlistRow).not.toContainText("every destination");
      // The control: the destination schedule still reads the way it always did.
      await expect(destRow).toContainText("every destination");
    } finally {
      expect(await deleteSchedule(page, playlistID)).toBe(200);
      expect(await deleteSchedule(page, destID)).toBe(200);
    }
  });

  test("playlist.start is badged as a start, not as a stop", async ({ page }) => {
    await signIn(page);
    // Both playlist actions, because the badge has to tell them apart. A guard
    // that only checked playlist.start would pass on a rule that badges every
    // playlist action live.
    const startID = await createSchedule(page, "e2e badge start", "playlist.start");
    const stopID = await createSchedule(page, "e2e badge stop", "playlist.stop");

    try {
      await openSchedules(page);
      const startBadge = page
        .getByRole("row")
        .filter({ hasText: "e2e badge start" })
        .getByText("playlist.start", { exact: true });
      const stopBadge = page
        .getByRole("row")
        .filter({ hasText: "e2e badge stop" })
        .getByText("playlist.stop", { exact: true });
      await expect(startBadge).toBeVisible();

      // Asserted on the RENDERED colour, not on a class name: the badge's
      // variant is an implementation detail that a design-token change may
      // rename, while "the start badge and the stop badge do not look the
      // same" is the thing an operator relies on.
      const startColour = await startBadge.evaluate((el) => getComputedStyle(el).backgroundColor);
      const stopColour = await stopBadge.evaluate((el) => getComputedStyle(el).backgroundColor);
      expect(startColour).not.toBe(stopColour);

      // And it matches the destination start, which is what "live" means here.
      const destID = await createSchedule(page, "e2e badge dest", "start");
      const destStart = page
        .getByRole("row")
        .filter({ hasText: "e2e badge dest" })
        .getByText("start", { exact: true });
      try {
        await openSchedules(page);
        await expect(destStart).toBeVisible();
        expect(await destStart.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe(
          startColour,
        );
      } finally {
        expect(await deleteSchedule(page, destID)).toBe(200);
      }
    } finally {
      expect(await deleteSchedule(page, startID)).toBe(200);
      expect(await deleteSchedule(page, stopID)).toBe(200);
    }
  });
});
