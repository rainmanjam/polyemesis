import { expect, type Page, test as setup } from "@playwright/test";
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
 * sides, and the calling script generates it per run rather than committing a
 * literal -- which is a password in a public repository however short-lived the
 * account it protects. Absent, this fails immediately and says so. */
const PASSWORD = process.env.E2E_PASSWORD;
if (!PASSWORD) {
  throw new Error(
    "E2E_PASSWORD is not set. scripts/capture-media.sh and " +
      "scripts/acceptance-browser.sh each generate one per run; export one " +
      "yourself to drive Playwright directly.",
  );
}

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

  await ensureSource(page);
});

/** Gives the install the one source the suite needs, if it has none.
 *
 *  A fresh install now comes up with NO source, which is the product decision
 *  this branch implements. Most of this suite does not care, but anything that
 *  compiles a destination does: POST /api/v1/destinations answers 503 no_source
 *  when there is no programme to send anywhere, because a destination with
 *  nothing upstream is not a thing the server can build.
 *
 *  Until now the suite got one BY ACCIDENT. edge-cases.spec.ts creates sources,
 *  and Playwright runs files in alphabetical order with one worker, so every
 *  spec from "edge-cases" onwards inherited them -- while
 *  destination-round-trip.spec.ts, sorting one place earlier, ran against an
 *  install with none and failed on the 503. The same accident props up
 *  polyemesis.spec.ts's "two sources cannot share a publish token", which
 *  requires more than one source to exist and would have nothing to compare if
 *  it ever sorted ahead of edge-cases. Renaming a spec file should not be able
 *  to break an unrelated one, so the programme is created here, once, before
 *  anything runs.
 *
 *  The ingest block is READ FROM SETTINGS rather than written out. The server's
 *  default mode is deliberately "unset" -- right for a product that must ask an
 *  operator, useless to a suite that must publish -- so only the mode is
 *  overridden. Spelling out a literal SRT block instead would mean carrying a
 *  latency, a passphrase policy and whatever the block gains next, and it would
 *  rot the first time one of them changed. */
async function ensureSource(page: Page) {
  const created = await page.evaluate(async () => {
    const csrf = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (csrf) headers["X-CSRF-Token"] = decodeURIComponent(csrf[1]);

    const list = await fetch("/api/v1/sources", { credentials: "same-origin" });
    if (!list.ok) throw new Error(`GET /api/v1/sources failed (${list.status})`);
    if (((await list.json()) as unknown[]).length > 0) return false;

    const settings = await fetch("/api/v1/settings", { credentials: "same-origin" });
    if (!settings.ok) throw new Error(`GET /api/v1/settings failed (${settings.status})`);
    const ingest = { ...((await settings.json()).ingest ?? {}), mode: "srt" };

    const res = await fetch("/api/v1/sources", {
      method: "POST",
      credentials: "same-origin",
      headers,
      body: JSON.stringify({ name: "E2E Programme", enabled: true, ingest }),
    });
    if (!res.ok) {
      throw new Error(`POST /api/v1/sources failed (${res.status}): ${await res.text()}`);
    }
    return true;
  });

  // Stated rather than silent: a run that inherited a source from a previous
  // pass against the same volume is a different starting state, and worth
  // seeing in the log when a later failure has to be explained.
  console.log(created ? "created the first source" : "an install with sources already; left alone");
}
