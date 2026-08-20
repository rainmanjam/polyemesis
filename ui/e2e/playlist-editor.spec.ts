import { execFileSync } from "node:child_process";
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

/** A real MPEG-TS, 1316 bytes: one black 16x16 h264 frame, no audio.
 *
 *  It used to be `new Blob(["polyemesis e2e fixture"])`, which stopped working
 *  the day uploads started being probed -- POST /api/v1/media now refuses
 *  anything ffprobe cannot read as media, and a text blob is exactly what that
 *  gate exists to catch. The fixture being fake was invisible until the server
 *  started checking.
 *
 *  Embedded rather than generated because this suite's only dependencies are
 *  docker and node; there is no ffmpeg on the host running it, and reaching
 *  into the container for one is the coupling that already makes another test
 *  in this file unrunnable against a remote install. */
const TINY_TS_BASE64 =
  "R0AREABC8CUAAcEAAP8B/wAB/IAUSBIBBkZGbXBlZwlTZXJ2aWNlMDF3fEPK////////////////////////////////" +
  "////////////////////////////////////////////////////////////////////////////////////////////" +
  "//////////////////////////////////////////////////////////////////9HQAAQAACwDQABwQAAAAHwACqx" +
  "BLL/////////////////////////////////////////////////////////////////////////////////////////" +
  "////////////////////////////////////////////////////////////////////////////////////////////" +
  "/////////////////////////////////////////0dQABAAArASAAHBAADhAPAAG+EA8AAVvU1W////////////////" +
  "////////////////////////////////////////////////////////////////////////////////////////////" +
  "////////////////////////////////////////////////////////////////////////////////////////////" +
  "////////////////R0EAMAdQAAB7DH4AAAAB4AAAgIAFIQAH2GEAAAABCfAAAAABZ0LACt3sBEAAAAMAQAAAAwCDxIng" +
  "AAAAAWjOD8gAAAEGBf//T9xF6b3m2Ui3lizYINkj7u94MjY0IC0gY29yZSAxNjUgcjMyMjIgYjM1NjA1YSAtIEguMjY0" +
  "L01QRUctNCBBVkMgY29kZWMgLSBDb3B5bGVmdCAyMDAzLTIwMjUgLSBodHRwOi8vd3d3LnZpZGVvbGFuLm9HAQARcmcv" +
  "eDI2NC5odG1sIC0gb3B0aW9uczogY2FiYWM9MCByZWY9MSBkZWJsb2NrPTA6LTM6LTMgYW5hbHlzZT0wOjAgbWU9ZGlh" +
  "IHN1Ym1lPTAgcHN5PTEgcHN5X3JkPTIuMDA6MC43MCBtaXhlZF9yZWY9MCBtZV9yYW5nZT0xNiBjaHJvbWFfbWU9MSB0" +
  "cmVsbGlzPTAgOHg4ZGN0PTAgY3FtPTAgZGVhZHpvbmU9MjEsMTEgZmFzdEcBABJfcHNraXA9MSBjaHJvbWFfcXBfb2Zm" +
  "c2V0PTAgdGhyZWFkcz0xIGxvb2thaGVhZF90aHJlYWRzPTEgc2xpY2VkX3RocmVhZHM9MCBucj0wIGRlY2ltYXRlPTEg" +
  "aW50ZXJsYWNlZD0wIGJsdXJheV9jb21wYXQ9MCBjb25zdHJhaW5lZF9pbnRyYT0wIGJmcmFtZXM9MCB3ZWlnaHRwPTAg" +
  "a2V5aW50PTEga2V5aW50X21pbj0xIHNjRwEAMz8A////////////////////////////////////////////////////" +
  "//////////////////////////////9lbmVjdXQ9MCBpbnRyYV9yZWZyZXNoPTAgcmM9Y3JmIG1idHJlZT0wIGNyZj0y" +
  "My4wIHFjb21wPTAuNjAgcXBtaW49MCBxcG1heD02OSBxcHN0ZXA9NCBpcF9yYXRpbz0xLjQwIGFxPTAAgAAAAWWIhDom" +
  "KAAJAuA=";

/** Uploads a small in-browser fixture via the same route the UI's uploader
 *  uses, and returns the STORED name -- uploads.SafeName appends a random hex
 *  suffix server-side, so the name the test must select in the picker is never
 *  the one it sent. */
async function uploadFixture(page: Page, hint: string): Promise<string> {
  return page.evaluate(async ({ fileHint, b64 }) => {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    const form = new FormData();
    form.append("file", new Blob([bytes], { type: "video/mp2t" }), fileHint);
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
  }, { fileHint: hint, b64: TINY_TS_BASE64 });
}

/** DELETEs an upload and returns the STATUS, which every caller then asserts
 *  on. It used to ignore the response entirely, and that is how the case below
 *  came to pass for the wrong reason: the in-use guard answers 409 for an
 *  upload a saved playlist item names, the refusal was silent, and a test that
 *  believed it had deleted a file went on to assert about a state it had never
 *  created. A helper whose failure is invisible is worse than no helper, because
 *  its callers write assertions believing it ran. */
async function deleteUpload(page: Page, name: string): Promise<number> {
  return page.evaluate(async (uploadName) => {
    const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
    const csrf = match ? decodeURIComponent(match[1]) : "";
    const res = await fetch(`/api/v1/media/${encodeURIComponent(uploadName)}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: csrf ? { "X-CSRF-Token": csrf } : {},
    });
    return res.status;
  }, name);
}

/** Removes an upload's FILE from the server's data directory without going
 *  through the API.
 *
 *  THIS IS THE ONLY ROUTE TO "a saved playlist item whose upload is missing",
 *  and that is a property of the product rather than an inconvenience.
 *  DELETE /api/v1/media/{name} refuses with 409 while a stored item names the
 *  upload, precisely so an operator cannot strand a playlist that way; and a
 *  save that INTRODUCES an item naming a file nobody has is refused with 400.
 *  So the API can refuse to CREATE this state and can still be asked to REPORT
 *  it -- exactly the distinction internal/api/playlist_status_test.go records,
 *  where the equivalent fixture is built with os.Remove for the same reason.
 *  What is left is how a real install reaches it: a sweep, a tidied disk, a
 *  restore that missed a file.
 *
 *  The suite runs against the shipped container (see e2e/playwright.config.ts
 *  and scripts/acceptance-browser.sh, which exports E2E_CONTAINER and mounts
 *  the data volume at /data), so `docker exec` is this suite's os.Remove. It
 *  throws on any failure rather than reporting one, so a container name that
 *  stops matching fails the test loudly instead of leaving it green against an
 *  upload that is still there. */
function removeUploadOutOfBand(name: string) {
  const container = process.env.E2E_CONTAINER ?? "poly-browser";
  // The DERIVATIVE goes too, and that is not tidiness.
  //
  // playlistItemStatus asks its questions in precedence order, and the first
  // one is "is there a non-empty derivative" -> ready, returning before it ever
  // stats the upload. So an item whose source file has been deleted still reads
  // READY as long as a normalised copy exists -- which is correct product
  // behaviour, because that is what the playlist would actually play.
  //
  // This test passed for years without removing it only because the fixture
  // was a text blob: normalisation failed, no derivative was ever written, and
  // the upload check was reached by accident. The moment the fixture became
  // real media the premise evaporated and this went red. Removing both is what
  // the test always meant.
  //
  // Globbed on the version rather than pinned to .v2.ts: ProfileVersion is
  // bumped whenever the profile changes, and a stale literal here would fail
  // open -- the derivative would survive, the row would read ready, and this
  // test would go green for the wrong reason all over again.
  execFileSync(
    "docker",
    [
      "exec",
      container,
      "sh",
      "-c",
      `rm -f -- '/data/uploads/${name}' /data/playlist-media/'${name}'.v*.ts`,
    ],
    { stdio: "pipe" },
  );
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
  // Still scoped by the one Card whose title is exactly "Failover", because the
  // controls INSIDE it -- Add, the item rows, the upload picker -- are
  // ambiguous page-wide.
  return page.locator(".rounded-lg.border", { has: page.getByText("Failover", { exact: true }) });
}

/** Commits the Pipeline tab.
 *
 *  THERE IS ONE SAVE NOW, and that is the point of it. This tab used to carry a
 *  Save button per card, each PUTting the whole tab draft -- so saving the
 *  failover slate also committed an abandoned chat-retention edit three cards
 *  away, and the MQTT card was the only one that carried the broker password,
 *  which every other button silently dropped while reporting success.
 *
 *  The card-scoped `getByRole("button", { name: "Save" })` this replaced was
 *  written to disambiguate between those eight. With one button and a banner
 *  that names the whole tab, scoping it to a card would now find nothing. */
async function savePipeline(page: Page) {
  await page.getByRole("button", { name: "Save pipeline settings" }).click();
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
  // exact, because getByRole matches an accessible name by SUBSTRING and
  // case-insensitively. The other buttons in this card are labelled after the
  // upload -- "Move <name> up", "Remove <name>" -- and these uploads are named
  // with a random hex digest. Hex is [0-9a-f], so a digest can contain the
  // letters "add", and when it does every one of those buttons matches this
  // locator and the click fails on a strict-mode violation.
  //
  // Observed: e2e-playlist-b-f22325d88fadd90f.ts resolved to 4 elements. It is
  // a dice roll, roughly one run in fifty, and it looks exactly like a real
  // regression when it lands.
  await card.getByRole("button", { name: "Add", exact: true }).click();
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

    await savePipeline(page);
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
    await savePipeline(page);
    await page.waitForTimeout(800);

    // 204 each, asserted: the items were removed and saved above, so the
    // in-use guard has nothing to refuse. A 409 here would mean the save did
    // not land, which is the same thing this test's reload assertion is about.
    expect(await deleteUpload(page, a)).toBe(204);
    expect(await deleteUpload(page, b)).toBe(204);
    expect(await deleteUpload(page, c)).toBe(204);
  });

  test("an item whose upload is missing is shown as needing attention", async ({ page }) => {
    await signIn(page);

    const gone = await uploadFixture(page, "e2e-playlist-gone.ts");

    await page.goto("/settings");
    await page.getByRole("tab", { name: "Pipeline" }).click();
    const foWas = await setSwitch(page, "#fo-enabled", true);
    const plWas = await setSwitch(page, "#fo-playlist", true);

    await addItem(page, gone);
    await savePipeline(page);
    await page.waitForTimeout(800);

    // The upload goes away OUT OF BAND -- see removeUploadOutOfBand. Calling
    // DELETE /api/v1/media here is what this test used to do, and it cannot
    // work: the item was saved 800 ms ago, so the in-use guard answers 409 and
    // the file stays exactly where it is.
    removeUploadOutOfBand(gone);

    // NO RELOAD, deliberately. The operator is standing on this page when the
    // file goes away, and the editor is supposed to notice: it polls readiness
    // while any item is unsettled. Reloading here would assert the same text
    // against a freshly mounted component and leave the catch-up -- which the
    // component fetched once at mount and never again until this branch --
    // with nothing watching it. Mutation: make the poll effect return
    // unconditionally and this row never changes.
    const row = page.locator('[data-testid="playlist-item"]', { hasText: gone });
    // Longer than the default. The fixture is real media now, so a
    // normalisation job queued at save actually runs, and while it is queued or
    // running the row reads "Transcoding" -- correctly, by the state
    // precedence. This has to outlast that job plus one poll interval.
    //
    // It used to say the fixture was a text blob and the wait was for a job
    // FAILURE. That was true, and it was also why this test was passing for the
    // wrong reason: a failed normalisation writes no derivative, so the ready
    // branch never fired and the missing-upload branch was reached by accident
    // rather than by the removal above.
    await expect(row.getByText("Needs attention")).toBeVisible({ timeout: 15_000 });
    // "no longer exists" is text ONLY the missing-upload branch produces
    // (internal/api/playlist_status.go). The assertion used to be
    // /upload|found/i, which the "normalisation failed: Invalid data found
    // when processing input" branch also satisfies -- so it stayed green on
    // the failed-normalisation state this fixture produces anyway, whether or
    // not the upload was ever missing. The mutation that must fail this: revert
    // playlistItemStatus's missing-upload branch.
    //
    // useInnerText, not the default toContainText: that one matches
    // textContent, which reads THROUGH display:none. The reason line is only
    // ever mounted for an attention row in this component, but asserting on
    // rendered text with the same option used everywhere else in this repo
    // that a "not visible" claim matters keeps this from silently degrading
    // into a textContent check if the row's markup ever changes to a
    // conditionally-hidden one instead of a conditionally-mounted one.
    await expect(row).toContainText(/no longer exists/i, { useInnerText: true });

    // Leave the shared install as it was found -- WITHOUT RELOADING FIRST, and
    // the absence of that reload is the assertion.
    //
    // A second save from one page load used to be refused with 400
    // "invalid request body: json: unknown field \"reload\"". PUT /settings
    // answers with api.settingsResponse, which embeds db.Settings and adds
    // `reload`; api.putSettings was typed put<Settings> and handed that whole
    // object back, so SettingsPage stored `reload` as settings state and the
    // next PUT carried it into a decoder with DisallowUnknownFields. Every
    // other spec happens to reload between its saves, so nothing had ever
    // asked -- and tsc could not, because the declared type said `Settings`
    // while the wire carried more.
    //
    // api.putSettings now strips it. Saving twice from one page load is the
    // ordinary thing an operator does, so this cleanup does it on purpose: put
    // the reload back and this test goes green while the bug is live again.
    const cleanup = failoverCard(page);
    await cleanup.getByRole("button", { name: `Remove ${gone}` }).click();
    if (!plWas) await setSwitch(page, "#fo-playlist", false);
    if (!foWas) await setSwitch(page, "#fo-enabled", false);
    await savePipeline(page);
    await page.waitForTimeout(800);

    // The fixture is deleted rather than left behind -- this file's header
    // promises the shared install is restored, and the first case does it. 404
    // rather than 204, and asserted as such: the file went out of band above,
    // so store.Delete finds nothing. The call still runs because the handler
    // sweeps every derivative version BEFORE it touches the upload, and a 204
    // here would mean the out-of-band removal never happened and this test
    // proved nothing.
    expect(await deleteUpload(page, gone)).toBe(404);
  });
});
