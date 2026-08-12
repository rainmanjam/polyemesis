import { expect, test, type Page } from "@playwright/test";

import { watchConsole } from "./console";
import {
  apiFetch,
  createFacebookDestination,
  type DestinationEnvelope,
  removeDestination,
} from "./destinations";

/* ===========================================================================
   The Facebook destination editor, driven rather than read.

   These are the guards issue #107 is about. They used to live in
   internal/db/facebook_ui_drift_test.go and compliance_drift_test.go, where
   they `os.ReadFile`d DestinationDialog.tsx and asserted on the text: that
   `id="dest-fb-donate"` appeared inside the span between
   `{platform === "facebook" && (` and its matching paren, that
   `setBackupIngestWanted(e.target.checked)` was written somewhere in the same
   span, that the save payload literal contained the word "facebook".

   Every one of those is a claim about what a browser puts on screen and what a
   click does, and source text cannot answer either. A block can be present,
   syntactically perfect and never mounted; a control can render and be wired to
   a state variable that never reaches the request. The guards knew it -- one of
   them carried a paragraph headed LIMITATION saying the honest version needed a
   browser and that ui/e2e/ was where it would go.

   This is that. What is asserted here is not that an element exists: an
   assertion of mere presence passes on a page that rendered an error boundary
   over the top of everything. Each test TYPES into the control, SAVES, and then
   reads the row back over the API, so the thing under test is the value's
   journey out of the dialog. watchConsole is on throughout, because a component
   that throws still leaves its neighbours on screen.

   What did NOT move here is the one claim a browser cannot make: that the
   audiences the select offers are the audiences the SERVER accepts. Go owns
   that enum. internal/db/compliance_drift_test.go pins ui/src/lib/facebookPrivacy.ts
   to db.FacebookPrivacies, and this file closes the loop from the other side --
   every option the select renders is saved and read back, so an option Go would
   reject fails here with a 400 rather than reaching an operator.
   =========================================================================== */

/** Opens the edit dialog for a destination by name, and fails loudly if the
 *  Facebook block is not in it -- the block is the subject of every test below,
 *  so "the dialog opened" is not enough to proceed on. */
async function openEditor(page: Page, name: string) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
  await page.getByRole("button", { name: `Actions for ${name}` }).click();
  // Exact: the same menu carries "Edit routing", a link to another page.
  await page.getByRole("menuitem", { name: "Edit", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  return dialog;
}

/** Stores one audience against a destination and returns the HTTP status,
 *  rather than throwing on a refusal. */
async function saveAudience(page: Page, id: number, value: string): Promise<number> {
  return page.evaluate(
    async ({ i, v }) => {
      const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
      const csrf = match ? decodeURIComponent(match[1]) : "";
      const res = await fetch(`/api/v1/destinations/${i}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", ...(csrf ? { "X-CSRF-Token": csrf } : {}) },
        body: JSON.stringify({ compliance: { facebookPrivacy: v } }),
      });
      return res.status;
    },
    { i: id, v: value },
  );
}

test.describe("the Facebook block writes what an operator types", () => {
  /* MUTATION RECORD -- the three controls issue #107 named, each broken in turn
     against the shipped container and each watched to fail BY NAME.
     Recorded because the rest of this repository records it: a guard whose
     failure nobody has seen is a guard nobody has any evidence about, which is
     the exact complaint #107 filed against the source-text guards these
     replaced. Two of the three are deliberately mutations a grep CANNOT see,
     because "a grep would have caught it too" would leave the move unjustified.

       donate    DestinationDialog.tsx: delete the <Input id="dest-fb-donate">,
                 leaving its <Label>. Measured: FAIL on the `.fill("424242")`
                 below, `waiting for getByRole('dialog').getByLabel('Donate
                 button charity ID')`. The other 5 tests in this file passed.

       crosspost DestinationDialog.tsx: change the Add Page onClick from
                 `crosspost: [...(facebook.crosspost ?? []), { pageId: "" }]`
                 to `crosspost: [...(facebook.crosspost ?? [])]` -- the button,
                 the placeholder and the .map all still there, the click just
                 appends nothing. INVISIBLE to source text: every string a
                 drift guard matched on is untouched. Measured: FAIL on the
                 pageId toBeVisible below, "clicking Add Page did not produce a
                 row to type a Page ID into". The other 5 passed.

       backup    DestinationDialog.tsx: drop `backupIngestWanted` from the PUT
                 payload literal. The checkbox still renders, still checks, both
                 cost sentences still show, and
                 `setBackupIngestWanted(e.target.checked)` is still written
                 verbatim -- so the old guard that grepped for that setter would
                 still pass. Measured: FAIL on the after.backupIngestWanted
                 assertion below, Expected true / Received undefined. The other
                 5 passed.

     Restored from a file copy after each; `git diff --stat` empty. */
  test("crosspost, donate, audience and the backup toggle all reach the server", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    const created = await createFacebookDestination(page, "e2e fb controls");

    try {
      const dialog = await openEditor(page, "e2e fb controls");

      // --- crosspost: the list starts empty, so the ADD button is the control.
      // A heading over an empty space is what the old guard could not tell apart
      // from a working list.
      await dialog.getByRole("button", { name: "Add Page" }).click();
      const pageId = dialog.getByPlaceholder("Page ID");
      await expect(
        pageId,
        "clicking Add Page did not produce a row to type a Page ID into, so the " +
          "crosspost list can only ever stay at the length it loaded with",
      ).toBeVisible();
      await pageId.fill("777000111");

      // --- donate charity id
      await dialog.getByLabel("Donate button charity ID").fill("424242");

      // --- the backup feed toggle, and the two costs it must state.
      //
      // Both sentences are asserted by their CONTENT, not by their presence.
      // The number is the whole point of the first one: an operator told a
      // backup "uses more bandwidth" will not plan for twice the upload, and
      // finds out during a broadcast.
      const backup = dialog.getByLabel("Publish a backup feed");
      await expect(backup).not.toBeChecked();
      await expect(dialog.getByText(/Doubles this destination.s upload bandwidth/)).toBeVisible();
      await expect(dialog.getByText(/reconnects the stream once/)).toBeVisible();
      await backup.check();

      // --- audience
      await dialog.getByTestId("fb-audience-picker").click();
      await page.getByRole("option", { name: "Friends of friends", exact: true }).click();

      await dialog.getByRole("button", { name: /save/i }).click();
      await expect(dialog).toBeHidden();

      const { destination: after } = await apiFetch<DestinationEnvelope>(
        page,
        "GET",
        `/api/v1/destinations/${created.id}`,
      );

      expect(
        after.facebook?.crosspost?.[0]?.pageId,
        "the crosspost Page the operator typed never left the dialog, so " +
          "Facebook crossposting is unreachable from the UI",
      ).toBe("777000111");
      expect(
        after.facebook?.donateCharityId,
        "the donate charity id never left the dialog, so " +
          "donate_button_charity_id never leaves polyemesis",
      ).toBe("424242");
      expect(
        after.backupIngestWanted,
        "the backup toggle never left the dialog, so the backup feed can never " +
          "be turned on from the UI",
      ).toBe(true);
      expect(
        after.compliance?.facebookPrivacy,
        "the audience the operator chose never left the dialog",
      ).toBe("FRIENDS_OF_FRIENDS");
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the dialog logged errors while being driven").toEqual([]);
  });

  // Every option the select offers must be one the server accepts.
  //
  // This is the browser half of the pin in internal/db/compliance_drift_test.go,
  // and the two halves are deliberately not the same assertion. Go proves that
  // every value in db.FacebookPrivacies appears in ui/src/lib/facebookPrivacy.ts,
  // which the select renders from. This proves the other direction, and proves
  // it by consequence rather than by comparison: each option is chosen, saved,
  // and read back. ValidFacebookPrivacy refuses anything the enum does not
  // carry with a 400, so an invented option fails here on the save.
  //
  // Reading the expected list out of facebookPrivacy.ts would make this
  // tautological -- the select renders from that file, so it would be comparing
  // the array to itself. The round trip is what makes it a test.
  test("every audience the select offers is one the server accepts", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    const created = await createFacebookDestination(page, "e2e fb audiences");

    try {
      const dialog = await openEditor(page, "e2e fb audiences");
      await dialog.getByTestId("fb-audience-picker").click();

      // The values the select is actually offering, read off the DOM rather
      // than assumed, so an option added to the UI and never wired is included
      // in the round trip below instead of quietly skipped.
      const offered = await page
        .getByRole("option")
        .evaluateAll((els) =>
          els.map((el) => el.getAttribute("data-value") ?? "").filter((v) => v && v !== "unset"),
        );
      await page.keyboard.press("Escape");

      expect(
        offered.length,
        "the audience select offers nothing but the leave-it-alone row, so no " +
          "operator can set a Facebook audience at all",
      ).toBeGreaterThan(0);

      for (const value of offered) {
        // The status, not a thrown "failed (400)". An option the server refuses
        // is the failure this test exists for, and it has to arrive naming the
        // option rather than as a stack trace from the fetch helper.
        const status = await saveAudience(page, created.id, value);
        expect(
          status,
          `the audience select offers ${value} and the server refused it (HTTP ${status}). ` +
            `ValidFacebookPrivacy rejects anything outside db.FacebookPrivacies, so an ` +
            `operator picking that audience meets an error with no field to attach it to`,
        ).toBe(200);

        const { destination: after } = await apiFetch<DestinationEnvelope>(
          page,
          "GET",
          `/api/v1/destinations/${created.id}`,
        );
        expect(
          after.compliance?.facebookPrivacy,
          `the select offers ${value} and the server did not store it -- an ` +
            `operator picking that audience gets a save that silently did nothing`,
        ).toBe(value);
      }
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the audience select logged errors").toEqual([]);
  });
});

/* The Page case.
 *
 * A Page broadcast is public: Facebook has no personal audience to restrict it
 * to, and IngestFor suppresses privacy for a Page target regardless of what is
 * stored. Offering the control anyway is a setting that silently does nothing.
 *
 * The Go guard that watched this could only say the words `!facebookTargetsAPage`
 * were written in front of the block, and said so itself: "this reads source
 * text, so it proves the gate is written, not that React renders it... The
 * honest guard for that is a browser test against a Page-connected account,
 * which needs an OAuth fixture this suite has no way to build."
 *
 * It does not need one. The dialog decides from `accountRef` on the connected
 * account, which arrives over /platforms/accounts -- so the fixture is a stubbed
 * response, and both branches become reachable. Stubbing the ACCOUNT rather than
 * the component is what keeps this a test of the real conditional: the same
 * DestinationDialog, the same useMemo, the same React. */
test.describe("a Page account is not offered a personal audience", () => {
  async function withAccount(page: Page, accountRef: string) {
    await page.route("**/api/v1/platforms/accounts", (route) =>
      route.fulfill({
        json: [
          {
            id: 4242,
            platform: "facebook",
            accountRef,
            accountName: "E2E stub account",
            scopes: [],
          },
        ],
      }),
    );
  }

  test("the audience control is replaced by an explanation for a Page", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    const created = await createFacebookDestination(page, "e2e fb page acct");

    try {
      await withAccount(page, "page:998877");
      const dialog = await openEditor(page, "e2e fb page acct");

      await dialog.getByTestId("connected-account-picker").click();
      await page.getByRole("option", { name: "E2E stub account" }).click();

      // The control is GONE, and something says why. Both halves: a setting
      // that vanishes with no statement of why reads as the form losing it.
      // The Page sentence, matched on the half that is unique to it. "a Page
      // broadcast is public" also appears in the hint UNDER the audience
      // control, so matching that phrase passes on the profile branch too --
      // measured, on the control case below, which is what it is there for.
      await expect(
        dialog.getByText(/There is no audience setting to make/),
        "an operator whose account publishes to a Page sees the audience control " +
          "disappear with no statement of why",
      ).toBeVisible();
      await expect(
        dialog.getByTestId("fb-audience-picker"),
        "a Page account is still offered a personal audience, which Facebook " +
          "ignores -- the operator sets it, sees it saved, and it never applies",
      ).toBeHidden();
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the Page branch logged errors").toEqual([]);
  });

  // The control case, and it is not decoration. Without it the assertion above
  // passes on a dialog whose audience control never renders for anyone -- which
  // is the unreachable-feature defect this whole file exists for, wearing the
  // costume of a fix.
  test("a profile account keeps the audience control", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    const created = await createFacebookDestination(page, "e2e fb profile acct");

    try {
      // A legacy ref carrying no `page:` prefix resolves as "auto" server-side,
      // trying Pages first and falling back to the profile, so which it is
      // cannot be known in the dialog. Those keep the control on purpose:
      // hiding it on a guess would take away a setting that does work.
      await withAccount(page, "user:112233");
      const dialog = await openEditor(page, "e2e fb profile acct");

      await dialog.getByTestId("connected-account-picker").click();
      await page.getByRole("option", { name: "E2E stub account" }).click();

      await expect(
        dialog.getByTestId("fb-audience-picker"),
        "a profile account is offered no audience control, so the Facebook " +
          "privacy setting is unreachable for the accounts it applies to",
      ).toBeVisible();
      await expect(dialog.getByText(/There is no audience setting to make/)).toBeHidden();
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the profile branch logged errors").toEqual([]);
  });
});

/* The server drops compliance a destination's platform cannot send and returns
 * a warning saying which. A drop nobody is shown is a setting that vanishes
 * between one save and the next open.
 *
 * That the server PRODUCES the warning is already covered, in Go, by
 * internal/api/unsendable_settings_test.go. The half nothing could see was the
 * consumption: `for (const w of warnings) toast.warning(w, ...)` was asserted by
 * matching that exact statement in the source, which proves the line is written
 * and not that anything appears on screen.
 *
 * The response is stubbed rather than provoked because the warning's text is
 * the server's, already tested as the server's, and the question here is
 * narrowly whether the dialog puts it in front of an operator. */
test.describe("the dialog shows what the server dropped", () => {
  test("a warning on save is raised as a toast carrying the server's words", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    const created = await createFacebookDestination(page, "e2e fb warnings");

    const dropped =
      "Compliance settings were removed: kick has no compliance surface, so a " +
      "privacy declaration stored here would never be sent.";

    try {
      await page.route(`**/api/v1/destinations/${created.id}`, async (route) => {
        if (route.request().method() !== "PUT") return route.fallback();
        const res = await route.fetch();
        const body = await res.json();
        return route.fulfill({ response: res, json: { ...body, warnings: [dropped] } });
      });

      const dialog = await openEditor(page, "e2e fb warnings");
      await dialog.getByLabel(/^name$/i).fill("e2e fb warnings renamed");
      await dialog.getByRole("button", { name: /save/i }).click();

      await expect(
        page.getByText(dropped),
        "the server said it had removed a setting and the dialog said nothing. " +
          "The operator's compliance settings vanish between one save and the " +
          "next open, with no explanation anywhere",
      ).toBeVisible();
    } finally {
      await page.unroute(`**/api/v1/destinations/${created.id}`);
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "raising the warning toast logged errors").toEqual([]);
  });
});
