import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   Browser end-to-end for the playlist control on the Settings page.

   Both cases exist because of the two ways this feature was shipped
   incomplete before: the drift guard
   (internal/db/settings_drift_test.go:TestUITypesCanNameEverySettingsField)
   only proves the UI can NAME every settings field, not that a control is
   reachable or that it reflects reality once saved. These cases close both
   gaps -- one for reachability (an operator can actually build the list),
   one for reflecting reality (an item that will not play SAYS SO, rather
   than saving silently and failing the first time failover reaches it).

   The install is shared and mutated in order with the rest of the suite (see
   e2e/playwright.config.ts), so each test restores the failover settings and
   deletes its own uploads before finishing -- leaving a stray "attention" item
   in the shared settings would make every later spec's /settings run trip
   over it.
   =========================================================================== */

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

/** Uploads a small in-browser fixture via the same route the UI's uploader
 *  uses, and returns the STORED name -- uploads.SafeName appends a random hex
 *  suffix server-side, so the name the test must select in the picker is never
 *  the one it sent. */
async function uploadFixture(page: Page, hint: string): Promise<string> {
  return page.evaluate(async (fileHint) => {
    const form = new FormData();
    form.append("file", new Blob(["polyemesis e2e fixture"], { type: "video/mp2t" }), fileHint);
    const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
    const csrf = match ? decodeURIComponent(match[1]) : "";
    const res = await fetch("/api/v1/media", {
      method: "POST",
      credentials: "same-origin",
      headers: csrf ? { "X-CSRF-Token": csrf } : {},
      body: form,
    });
    if (!res.ok) throw new Error(`fixture upload failed (${res.status})`);
    const body = (await res.json()) as { name: string };
    return body.name;
  }, hint);
}

async function deleteUpload(page: Page, name: string) {
  await page.evaluate(async (uploadName) => {
    const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
    const csrf = match ? decodeURIComponent(match[1]) : "";
    await fetch(`/api/v1/media/${encodeURIComponent(uploadName)}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: csrf ? { "X-CSRF-Token": csrf } : {},
    });
  }, name);
}

/** Clicks a switch only if it is not already in the wanted state, and reports
 *  what it was. Idempotent, because this suite is serial and a prior run's
 *  leftover state must not change what this test asserts about its OWN
 *  transitions. */
async function setSwitch(page: Page, id: string, want: boolean): Promise<boolean> {
  const el = page.locator(id);
  const was = (await el.getAttribute("data-state")) === "checked";
  if (was !== want) await el.click();
  return was;
}

function failoverCard(page: Page) {
  // Scoped by the one Card whose title is exactly "Failover" -- there are
  // several Save buttons on this page, one per settings card, and an
  // unscoped getByRole("button", { name: "Save" }) would be ambiguous about
  // which one it clicked.
  return page.locator(".rounded-lg.border", { has: page.getByText("Failover", { exact: true }) });
}

function itemNames(page: Page) {
  return page.locator('[data-testid="playlist-item"] .font-mono').allInnerTexts();
}

async function addItem(page: Page, upload: string) {
  const card = failoverCard(page);
  await card.getByRole("combobox", { name: "Choose an upload to add" }).click();
  // The option list renders through a Radix Portal, outside the card's own
  // DOM subtree -- a card-scoped locator here would never find it.
  await page.getByRole("option", { name: upload }).click();
  await card.getByRole("button", { name: "Add" }).click();
}

test.describe("playlist editor", () => {
  test("an operator can add, reorder and remove playlist items, and the order persists across a reload", async ({
    page,
  }) => {
    await signIn(page);

    const a = await uploadFixture(page, "e2e-playlist-a.ts");
    const b = await uploadFixture(page, "e2e-playlist-b.ts");
    const c = await uploadFixture(page, "e2e-playlist-c.ts");

    await page.goto("/settings");
    // The Failover card lives under the "Pipeline" tab, which is not the
    // default -- Radix Tabs does not mount an inactive panel's content at
    // all, so #fo-enabled does not exist in the DOM until this is clicked.
    await page.getByRole("tab", { name: "Pipeline" }).click();

    const card = failoverCard(page);
    const foWas = await setSwitch(page, "#fo-enabled", true);
    const plWas = await setSwitch(page, "#fo-playlist", true);

    await addItem(page, a);
    await addItem(page, b);
    await addItem(page, c);
    await expect.poll(() => itemNames(page)).toEqual([a, b, c]);

    // Reorder: move C up past B. If the move buttons swapped the wrong pair,
    // or mutated in place instead of returning a new array so React never
    // re-rendered, this is where it would show up.
    await card.getByRole("button", { name: `Move ${c} up` }).click();
    await expect.poll(() => itemNames(page)).toEqual([a, c, b]);

    // Remove the item now at the end. The remaining two-item order is what
    // the reload assertion below depends on to prove SAVE actually reached
    // the server, rather than the draft merely looking right in memory.
    await card.getByRole("button", { name: `Remove ${b}` }).click();
    await expect.poll(() => itemNames(page)).toEqual([a, c]);

    await card.getByRole("button", { name: "Save" }).click();
    // No toast is asserted here on purpose -- Settings' own save button
    // already has coverage for the request completing; this waits for the
    // draft's own PUT to have landed before reloading, the same debounce
    // margin the "a source rename persists" case uses.
    await page.waitForTimeout(800);
    await page.reload();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    await expect.poll(() => itemNames(page)).toEqual([a, c]);

    // Leave the shared install as it was found.
    await card.getByRole("button", { name: `Remove ${c}` }).click();
    await card.getByRole("button", { name: `Remove ${a}` }).click();
    await expect.poll(() => itemNames(page)).toEqual([]);
    if (!plWas) await setSwitch(page, "#fo-playlist", false);
    if (!foWas) await setSwitch(page, "#fo-enabled", false);
    await card.getByRole("button", { name: "Save" }).click();
    await page.waitForTimeout(800);

    await deleteUpload(page, a);
    await deleteUpload(page, b);
    await deleteUpload(page, c);
  });

  test("an item whose upload is missing is shown as needing attention", async ({ page }) => {
    await signIn(page);

    const gone = await uploadFixture(page, "e2e-playlist-gone.ts");

    await page.goto("/settings");
    await page.getByRole("tab", { name: "Pipeline" }).click();
    const card = failoverCard(page);
    const foWas = await setSwitch(page, "#fo-enabled", true);
    const plWas = await setSwitch(page, "#fo-playlist", true);

    await addItem(page, gone);
    await card.getByRole("button", { name: "Save" }).click();
    await page.waitForTimeout(800);

    // Delete the upload out from under the saved playlist item -- the exact
    // way a real install ends up with a broken entry: nothing edits the
    // playlist itself, an upload it already named just stops existing.
    await deleteUpload(page, gone);
    await page.reload();
    await page.getByRole("tab", { name: "Pipeline" }).click();

    const row = page.locator('[data-testid="playlist-item"]', { hasText: gone });
    await expect(row.getByText("Needs attention")).toBeVisible();
    // useInnerText, not the default toContainText: that one matches
    // textContent, which reads THROUGH display:none. The reason line is only
    // ever mounted for an attention row in this component, but asserting on
    // rendered text with the same option used everywhere else in this repo
    // that a "not visible" claim matters keeps this from silently degrading
    // into a textContent check if the row's markup ever changes to a
    // conditionally-hidden one instead of a conditionally-mounted one.
    await expect(row).toContainText(/upload|found/i, { useInnerText: true });

    // Leave the shared install as it was found.
    await card.getByRole("button", { name: `Remove ${gone}` }).click();
    if (!plWas) await setSwitch(page, "#fo-playlist", false);
    if (!foWas) await setSwitch(page, "#fo-enabled", false);
    await card.getByRole("button", { name: "Save" }).click();
    await page.waitForTimeout(800);
  });
});
