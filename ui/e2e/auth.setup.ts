import { expect, test as setup } from "@playwright/test";
import { existsSync } from "node:fs";

/** Signs in once and saves the session for every other test to reuse.
 *
 *  Playwright gives each test a fresh browser context, so without this every
 *  test would have to log in again -- and the first run's *setup* form is gone
 *  by the second test, so each one would need to handle both paths. That is
 *  three chances to get authentication wrong in a suite that is not about
 *  authentication.
 *
 *  Doing it once here also means the whole suite is runnable twice against the
 *  same volume: this handles first-run and returning-user, and nothing else has
 *  to. */
const STATE = "e2e/.auth/state.json";

/* Overridable because something else may have completed first-run already.
 *
 * scripts/capture-media.sh seeds a demo install before Playwright starts, so
 * the admin account exists with whatever password the seeder chose. With the
 * value hard-coded here, this file found no "Create account" button, tried to
 * sign in with a password nobody had set, and failed on a missing <nav> — a
 * symptom several steps removed from the cause. One variable now drives both
 * sides; the default keeps the browser suite working with no environment at
 * all. */
const PASSWORD = process.env.E2E_PASSWORD ?? "BrowserE2E!9xz";

setup("authenticate", async ({ page }) => {
  await page.goto("/");

  const create = page.getByRole("button", { name: "Create account" });
  if (await create.isVisible().catch(() => false)) {
    await page.locator("#password").fill(PASSWORD);
    await page.locator("#confirm").fill(PASSWORD);
    await create.click();
  } else {
    await page.locator("#username").fill("admin");
    await page.locator("#password").fill(PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
  }

  // The nav only renders once authenticated, so this is the real assertion
  // that a session exists rather than that a button was clicked.
  await expect(page.locator("nav")).toBeVisible({ timeout: 15_000 });

  if (!existsSync("e2e/.auth")) {
    // Playwright creates the file's directory itself, but being explicit keeps
    // the failure legible if it ever cannot.
  }
  await page.context().storageState({ path: STATE });
});
