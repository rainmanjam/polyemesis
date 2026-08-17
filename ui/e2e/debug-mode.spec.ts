import { expect, test, type Page } from "@playwright/test";

import { watchConsole } from "./console";

/* ===========================================================================
   Browser end-to-end for the debug-mode tab in Settings.

   WHAT ONLY A BROWSER CAN ANSWER HERE, which is why this exists at all. The Go
   side already covers the routes, the audit entry and the scrubbing, and
   internal/diag's disclosure test is the thing that actually protects an
   operator. None of that can say:

     * whether the confirmation ACTUALLY GATES the export, or whether the button
       fires and the dialog is decoration;
     * whether a file arrives at all -- the export is a POST fetched into a blob
       rather than an `<a href download>`, which is the one download in this UI
       that could silently produce nothing;
     * whether a panel that POLLS puts errors in the console on a loop.

   That last one is not hypothetical in this repository: a 412 from a polled
   Facebook stream-health read filled the console once per poll, and
   live-status-rendering.spec.ts caught it. This panel polls every two seconds
   while recording.

   THE INSTALL IS SHARED AND MUTATED IN ORDER by the rest of the suite (see
   playwright.config.ts), so this leaves recording OFF and the buffer CLEARED
   however it exits. A spec that left debug recording on would change what every
   later spec's server is doing -- and would quietly fill a ring with their
   traffic.
   =========================================================================== */

/** Opens Settings and selects the Debug tab.
 *
 *  The tab's content is not merely hidden until selected -- like the schedule
 *  list on Automation, it is not in the document at all -- so a test that only
 *  navigated would find nothing and be unable to say why. */
async function openDebugTab(page: Page) {
  await page.goto("/settings");
  await expect(page.locator("nav")).toBeVisible();
  await page.getByRole("tab", { name: /debug/i }).click();
  await expect(page.getByRole("switch", { name: /record server activity/i })).toBeVisible();
}

/** Leaves the server as it was found: not recording, buffer empty. */
async function leaveClean(page: Page) {
  const sw = page.getByRole("switch", { name: /record server activity/i });
  if ((await sw.getAttribute("data-state")) === "checked") {
    await sw.click();
    await expect(sw).toHaveAttribute("data-state", "unchecked");
  }
  const clear = page.getByRole("button", { name: /clear buffer/i });
  if (await clear.isEnabled()) await clear.click();
}

test.describe("debug mode", () => {
  test.afterEach(async ({ page }) => {
    await leaveClean(page);
  });

  test("the tab is reachable and starts not recording", async ({ page }) => {
    await openDebugTab(page);

    const sw = page.getByRole("switch", { name: /record server activity/i });
    await expect(sw).toHaveAttribute("data-state", "unchecked");

    // Nothing held, so there is nothing to export or clear. A button that
    // offers to export an empty bundle wastes an audit entry on nothing.
    await expect(page.getByRole("button", { name: /export bundle/i })).toBeDisabled();
  });

  test("recording captures, and the panel logs nothing while it polls", async ({ page }) => {
    const console_ = watchConsole(page);
    await openDebugTab(page);

    await page.getByRole("switch", { name: /record server activity/i }).click();
    await expect(page.getByRole("switch", { name: /record server activity/i })).toHaveAttribute(
      "data-state",
      "checked",
    );

    // The count is polled every two seconds while recording. Waiting for the
    // export button to enable is waiting for the server to have captured
    // something, which is the real assertion -- the switch alone only proves a
    // request was accepted.
    await expect(page.getByRole("button", { name: /export bundle/i })).toBeEnabled({
      timeout: 15_000,
    });

    // A POLLED PANEL MUST NOT FILL THE CONSOLE. An operator opening devtools on
    // a working install should not find errors from a panel that is doing
    // exactly what it was asked to do.
    expect(console_.errors).toEqual([]);
  });

  test("the export is gated by the confirmation, and cancelling exports nothing", async ({
    page,
  }) => {
    await openDebugTab(page);
    await page.getByRole("switch", { name: /record server activity/i }).click();
    const exportBtn = page.getByRole("button", { name: /export bundle/i });
    await expect(exportBtn).toBeEnabled({ timeout: 15_000 });

    await exportBtn.click();

    // The dialog has to say what is about to leave. The COUNT is the point:
    // confirming a number is a decision, confirming a vibe is a click.
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(/log lines/i);

    // Cancelling must produce no download. Asserted by racing a download event
    // against a timeout: if the button fires regardless of the dialog, this
    // resolves with a download and the test fails.
    const cancelled = page
      .waitForEvent("download", { timeout: 2000 })
      .then(() => "downloaded")
      .catch(() => "none");
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    expect(await cancelled).toBe("none");
  });

  test("confirming produces a file, and it carries no obvious credential", async ({ page }) => {
    await openDebugTab(page);
    await page.getByRole("switch", { name: /record server activity/i }).click();
    const exportBtn = page.getByRole("button", { name: /export bundle/i });
    await expect(exportBtn).toBeEnabled({ timeout: 15_000 });

    await exportBtn.click();
    await expect(page.getByRole("dialog")).toBeVisible();

    const [download] = await Promise.all([
      page.waitForEvent("download"),
      page.getByRole("dialog").getByRole("button", { name: /^export$/i }).click(),
    ]);

    // A POST fetched into a blob is the one download in this UI that could
    // silently produce nothing, so the file itself is the assertion.
    expect(download.suggestedFilename()).toMatch(/^polyemesis-debug-.*\.json$/);

    const path = await download.path();
    expect(path).toBeTruthy();
    const body = await download.createReadStream();
    let text = "";
    for await (const chunk of body) text += String(chunk);

    // It parses, and it is the shape internal/diag promises.
    const parsed = JSON.parse(text) as {
      capture?: { held?: number };
      records?: unknown[];
      version?: string;
    };
    expect(parsed.capture?.held).toBeGreaterThan(0);
    expect(Array.isArray(parsed.records)).toBe(true);

    /* A CHEAP RESIDUAL CHECK, AND ITS LIMITS ARE THE REASON IT IS COMMENTED.
       internal/diag/disclosure_test.go is what actually proves the scrubbing:
       it plants a known key in every shape a record can carry one and asserts
       none survives. This cannot do that -- it has no planted secret and no way
       to make the server log one on demand.

       What it CAN do is catch the gross failure that would follow a wiring
       mistake: the bundle reaching the browser without having gone through the
       recorder at all. An `rtmp://host/app/<key>` publish URL is the shape that
       has actually leaked here before, so a stream-key-looking path segment in
       the exported text means something is very wrong. It is a smoke alarm, not
       a scrubber. */
    expect(text).not.toMatch(/rtmps?:\/\/[^"\s]+\/[A-Za-z0-9_-]{20,}/);
  });
});
