import { expect, test, type Page } from "@playwright/test";

import { watchConsole } from "./console";

/* ===========================================================================
   The Go live composer, driven rather than read.

   These are the two claims issue #259 is about. They used to live in
   internal/oauth/composer_tags_drift_test.go, where they `os.ReadFile`d
   ui/src/pages/Dashboard.tsx and asserted on the text: that the 400 characters
   after `metaFetch<MetaJob>("/metadata/push"` contained the substring "tags",
   and that the strings `withCompliance.length > 0` and
   `withCompliance.length === 0` appeared somewhere in the file.

   Both are claims about what a browser puts on screen and what a click sends,
   and source text can answer neither. `tags` appearing inside a 400-character
   window proves a literal is written near a fetch; it does not prove the
   composer renders an input, that the input is reachable, that its value
   reaches the state, or that the state reaches the body. `withCompliance.length
   > 0` proves a conditional is written; the branch it guards can be inside a
   component that never mounts. #107's whole finding was that a guard of this
   shape passes on a page that rendered an error boundary over the top of
   everything, and #259 is the same defect one package over.

   THE STATED BLOCKER WAS RE-TESTED, NOT ASSUMED, because #258 found that
   assumption wrong three times. The claim would have been that these need
   connected platform accounts and therefore an OAuth fixture this suite cannot
   build. They do not. The composer decides everything from what
   GET /metadata returns -- which platforms are targets, which fields each
   accepts, and what compliance is stored against each -- so a stubbed response
   reaches every branch with the REAL GoLiveComposer, the real useState, the
   real React. Stubbing the account listing rather than the component is what
   keeps these tests of the real conditionals; it is the same technique
   facebook-destination.spec.ts uses for the Page-account branch.

   What is asserted is never mere presence. The tags test TYPES into the input,
   clicks Push, and reads the body that left the browser. The compliance test
   reads the SENTENCE's content -- which account it names -- and drives the
   button whose disabled state is the second half of the claim, then runs the
   whole thing again with compliance removed so that a composer which always
   said it, or never enabled the button, fails.
   =========================================================================== */

/** One entry of GET /api/v1/metadata's `targets`, mirroring internal/api's
 *  metadataTarget and the MetaTarget interface in Dashboard.tsx. */
interface StubTarget {
  accountId: number;
  platform: string;
  accountName: string;
  caps: { fields: string[]; titleMax?: number; categoryHint?: string };
  compliance?: {
    privacy?: string;
    madeForKids?: boolean;
    labels?: Record<string, boolean>;
    facebookPrivacy?: string;
  };
}

/** Answers the two reads the composer makes on mount.
 *
 *  broadcast-window is stubbed as well as /metadata, and deliberately as an
 *  EMPTY list rather than left to the real server. The composer's tags control
 *  is gated on caps.fields, not on the broadcast window -- Facebook resolves
 *  tags through top-level Metadata.Tags and has no broadcast resource at all --
 *  so answering "no broadcast accounts" is what proves that gating is what it
 *  claims to be. If the tags input only appeared when a broadcast account did,
 *  the Facebook-only install would have no way to set tags and this test would
 *  fail on the missing input rather than passing quietly. */
async function withTargets(page: Page, targets: StubTarget[]) {
  await page.route("**/api/v1/metadata", (route) =>
    route.fulfill({ json: { targets } }),
  );
  await page.route("**/api/v1/metadata/broadcast-window", (route) =>
    route.fulfill({ json: { accounts: [] } }),
  );
}

/** The composer card, so nothing below matches a control on another panel.
 *
 *  Opening the dashboard is folded in because the composer only renders once
 *  /metadata has answered -- GoLiveComposer returns null while targets is
 *  still null -- so waiting for this card IS waiting for the stub to have been
 *  consumed by the real component. */
async function openComposer(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
  const card = page.getByTestId("golive-composer");
  await expect(
    card,
    "the Go live composer never rendered, so nothing below is being asserted " +
      "about the control it names",
  ).toBeVisible();
  return card;
}

test.describe("the composer sends the tags an operator types", () => {
  /* The claim TestTheComposerCanSendFacebookTags was making, and why it is not
   * the same as "MetaField lists tags".
   *
   * A push result has been able to NAME a tags field since before anything
   * could set one, so the older guard over MetaField was green while the
   * feature was unreachable. Offering an operator a way to fill a field and
   * naming that field in a result are different claims, and only the first
   * makes a feature exist. This drives the first one end to end: the input is
   * found by its LABEL, typed into, and the bytes that leave the browser are
   * read off the wire. */
  test("a typed tag list leaves the browser in the push body", async ({ page }) => {
    const console_ = watchConsole(page);

    // Facebook, because Facebook is the case the Go guard was written for: it
    // takes tags through top-level Metadata.Tags rather than through a
    // broadcast resource, and it is the platform whose whole tag-resolution
    // path was unreachable while the composer sent none.
    await withTargets(page, [
      {
        accountId: 4242,
        platform: "facebook",
        accountName: "E2E stub page",
        caps: { fields: ["title", "description", "tags"], titleMax: 255 },
      },
    ]);

    // Captured rather than asserted inside the route handler: an expect() that
    // throws in a Playwright route callback fails the request instead of the
    // test, and the test then dies on a timeout naming nothing.
    let body: Record<string, unknown> | null = null;
    await page.route("**/api/v1/metadata/push", async (route) => {
      body = route.request().postDataJSON();
      // Fulfilled with a finished job so the composer's poller never starts and
      // the result pane can be read below. The job's CONTENT is this stub's,
      // so nothing is asserted about what the platforms did -- only that the
      // composer completed the round trip without throwing.
      return route.fulfill({
        json: {
          id: "e2e-job",
          done: true,
          metadata: { title: "E2E broadcast", description: "", category: "" },
          results: [
            {
              accountId: 4242,
              platform: "facebook",
              accountName: "E2E stub page",
              state: "ok",
              applied: ["title", "tags"],
            },
          ],
        },
      });
    });

    const card = await openComposer(page);

    // The INPUT must exist and be reachable. Found by its label rather than by
    // id, because a label is what an operator navigates by and a control with
    // no accessible name is a control they cannot find.
    const tags = card.getByLabel("Tags", { exact: true });
    await expect(
      tags,
      "the composer offers no tags input at all, so Metadata.Tags is always " +
        "empty in production and every line of tag resolution is unreachable",
    ).toBeVisible();

    await card.getByLabel("Title", { exact: true }).fill("E2E broadcast");
    // Trailing and doubled separators on purpose. The composer drops blanks
    // because an empty entry makes Facebook's resolver search for nothing, and
    // that is behaviour worth pinning rather than a tidiness detail.
    await tags.fill("house, , dj set ,");

    await card.getByRole("button", { name: "Push to platforms" }).click();
    await expect(card.getByText("Updated")).toBeVisible();

    const sent = body as Record<string, unknown> | null;
    expect(
      sent,
      "clicking Push sent no request to /metadata/push at all",
    ).not.toBeNull();
    expect(
      sent!.tags,
      "the push body carries no top-level tags field, so Metadata.Tags is " +
        "always empty and Facebook's tag resolution is unreachable from the UI",
    ).toEqual(["house", "dj set"]);
    // The OTHER destination for the same list. YouTube and Kick take tags
    // through PushBroadcastSettings, keyed to a broadcast resource Facebook
    // does not have, so a composer that filled only one of the two would leave
    // half the platforms unable to receive a tag the operator typed.
    expect(
      (sent!.broadcast as Record<string, unknown> | undefined)?.tags,
      "the push body's broadcast block carries no tags, so YouTube and Kick " +
        "receive none of what the operator typed",
    ).toEqual(["house", "dj set"]);

    expect(console_.errors, "the composer logged errors while being driven").toEqual([]);
  });

  /* THE CONTROL, and it is not decoration.
   *
   * Without it the assertion above passes on a composer that sends a hardcoded
   * tag list, or one that sends the same list whatever the input holds. An
   * empty field must produce NO tags key rather than an empty array: the API
   * takes a pointer precisely so "leave them alone" and "clear them" are
   * different requests, and sending [] for an untouched field would wipe the
   * tags on every platform every time anyone pushed a title. */
  test("an untouched tags field sends no tags at all, rather than an empty list", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await withTargets(page, [
      {
        accountId: 4242,
        platform: "facebook",
        accountName: "E2E stub page",
        caps: { fields: ["title", "tags"], titleMax: 255 },
      },
    ]);

    let body: Record<string, unknown> | null = null;
    await page.route("**/api/v1/metadata/push", async (route) => {
      body = route.request().postDataJSON();
      return route.fulfill({
        json: {
          id: "e2e-job-2",
          done: true,
          metadata: { title: "Title only", description: "", category: "" },
          results: [],
        },
      });
    });

    const card = await openComposer(page);
    await card.getByLabel("Title", { exact: true }).fill("Title only");
    await card.getByRole("button", { name: "Push to platforms" }).click();

    await expect
      .poll(() => body, { message: "clicking Push sent no request" })
      .not.toBeNull();
    const sent = body as Record<string, unknown> | null;
    expect(
      "tags" in sent! ? sent!.tags : undefined,
      "an untouched tags field sends a tags value anyway, so every push of a " +
        "title alone would REPLACE the tags already set on each platform -- the " +
        "composer's own hint says these replace rather than add",
    ).toBeUndefined();

    expect(console_.errors, "the composer logged errors while being driven").toEqual([]);
  });
});

test.describe("the composer says when a push carries stored compliance", () => {
  /* The claim TestTheComposerSaysWhenAPushCarriesStoredCompliance was making.
   *
   * Compliance is configured per DESTINATION and has no field in the composer,
   * so without this an operator presses Push and a COPPA declaration, a privacy
   * setting or a set of content labels goes out with nothing on screen having
   * mentioned it. A push that does MORE than it says is the same complaint as
   * one that does less, and this half is the harder one to notice.
   *
   * The second half is the BUTTON. `empty` disables Push, and a version
   * computing it from the typed fields alone would leave the server's own
   * allowance for a compliance-only push unreachable -- an operator whose
   * entire purpose is applying a stored COPPA declaration types nothing here.
   *
   * Both halves are asserted twice: once with compliance stored and once
   * without. A sentence that is always on screen and a button that is never
   * disabled both satisfy the first pass on their own. */
  const withStored: StubTarget[] = [
    {
      accountId: 11,
      platform: "youtube",
      accountName: "Compliant channel",
      caps: { fields: ["title", "description", "madeForKids"] },
      compliance: { madeForKids: true },
    },
    {
      accountId: 22,
      platform: "twitch",
      accountName: "Plain channel",
      caps: { fields: ["title"] },
    },
  ];
  const withoutStored: StubTarget[] = withStored.map((tgt) => ({
    ...tgt,
    compliance: undefined,
  }));

  test("with compliance stored, the composer names the account and Push stays available", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await withTargets(page, withStored);
    const card = await openComposer(page);

    // The sentence, matched on the ACCOUNT NAME it derives rather than on the
    // fixed prose around it. The fixed prose is present whatever the data is;
    // the name is the part that proves withCompliance was actually computed
    // from the targets, and naming the WRONG account is the failure an operator
    // would act on incorrectly.
    const notice = card.getByText(/This push also sends the compliance settings stored on/);
    await expect(
      notice,
      "the composer never mentions stored compliance, so a push sends a COPPA " +
        "declaration or a privacy setting with nothing on screen having said so",
    ).toBeVisible();
    await expect(notice).toContainText("Compliant channel");
    await expect(
      notice,
      "the notice names an account that has no compliance stored, so an " +
        "operator is told a setting will go out that will not",
    ).not.toContainText("Plain channel");

    // The button, with EVERY field untouched. This is the compliance-only push
    // the server allows, and a composer computing `empty` from the typed fields
    // alone would make it unreachable.
    const push = card.getByRole("button", { name: "Push to platforms" });
    await expect(
      push,
      "the Push button is disabled with compliance stored and nothing typed, so " +
        "a compliance-only push cannot be started even though the server allows it",
    ).toBeEnabled();

    expect(console_.errors, "the composer logged errors while being driven").toEqual([]);
  });

  test("with no compliance stored, the notice is gone and Push is disabled", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await withTargets(page, withoutStored);
    const card = await openComposer(page);

    await expect(
      card.getByText(/This push also sends the compliance settings stored on/),
      "the composer announces stored compliance for accounts that have none, so " +
        "the notice says nothing about this install and an operator learns to " +
        "ignore it",
    ).toBeHidden();

    const push = card.getByRole("button", { name: "Push to platforms" });
    await expect(
      push,
      "the Push button is enabled with nothing typed and nothing stored, so a " +
        "push that would send an empty title to every platform is one click away",
    ).toBeDisabled();

    // And it becomes available the moment there IS something to send, which is
    // what stops the assertion above from passing on a button that is disabled
    // forever.
    await card.getByLabel("Title", { exact: true }).fill("Something to say");
    await expect(push).toBeEnabled();

    expect(console_.errors, "the composer logged errors while being driven").toEqual([]);
  });
});
