import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   The destination editor must give back everything it was given.

   It was written for a mutation a review reported as surviving every guard on
   the branch that added Facebook's create-time settings:
   `setFacebook(destination.facebook ?? {})` -> `setFacebook({})`, supposedly
   wiping a configured block the next time an operator saved the destination.

   THAT MUTATION IS A NO-OP, and finding out why was worth more than the guard
   would have been. handleUpdateDestination decodes over the EXISTING row, so a
   body of {"facebook":{}} unmarshals an empty object into an already-populated
   struct and sets nothing at all. The block survives. Established by running
   the mutation, twice -- the review reasoned it through and reached the wrong
   answer, and so did I when I wrote this test to catch it.

   What it does guard is real and was untested anywhere: that editing ONE field
   on a destination leaves every other stored block alone. That depends on the
   merge above and on the dialog handing back what it loaded, and the guards
   around it are string-presence checks on source text -- they prove a field is
   wired and a control is reachable, and cannot see a load path that drops what
   it read.

   The rename is the control, and it is not decoration. The first version saved
   the dialog untouched and asserted the blocks survived; it passed even with
   the load path mutated, because nothing proved a save had happened at all.
   Changing one field gives the assertions something that MUST move.

   The pre-existing compliance block rides along in the same assertion, because
   proving this for one JSON column and not the identical one beside it is how
   the neighbour gets missed.

   =========================================================================== */

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

/** Runs a fetch inside the page so it carries the session cookie and CSRF
 *  token, the way every other spec in this suite reaches the API. */
async function api<T>(page: Page, method: string, path: string, body?: unknown): Promise<T> {
  return page.evaluate(
    async ({ m, p, b }) => {
      const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
      const csrf = match ? decodeURIComponent(match[1]) : "";
      const res = await fetch(p, {
        method: m,
        credentials: "same-origin",
        headers: {
          ...(b === undefined ? {} : { "Content-Type": "application/json" }),
          ...(csrf ? { "X-CSRF-Token": csrf } : {}),
        },
        body: b === undefined ? undefined : JSON.stringify(b),
      });
      if (!res.ok) throw new Error(`${m} ${p} failed (${res.status})`);
      return res.status === 204 ? null : await res.json();
    },
    { m: method, p: path, b: body },
  );
}

/** Both endpoints wrap the row: {"destination": {...}}, alongside a compiled
 *  routing block on the read. Unwrapped here so the assertions below read as
 *  statements about the destination rather than about the envelope. */
interface DestinationEnvelope {
  destination: Destination;
}

interface Destination {
  id: number;
  name: string;
  compliance?: { facebookPrivacy?: string };
  facebook?: {
    crosspost?: { pageId: string; createPost?: boolean }[];
    donateCharityId?: string;
  };
}

test.describe("the destination editor preserves what it loaded", () => {
  test("saving a Facebook destination unchanged keeps its settings", async ({ page }) => {
    await signIn(page);

    const { destination: created } = await api<DestinationEnvelope>(page, "POST", "/api/v1/destinations", {
      name: "e2e round trip",
      kind: "rtmp",
      platform: "facebook",
      url: "rtmps://live-api.facebook.com:443/rtmp/",
      streamKey: "e2e-key",
      // Both JSON-blob columns, set together on purpose: they share a load path
      // and a blind spot, so proving one and not the other would be an accident
      // of which field somebody happened to be looking at.
      compliance: { facebookPrivacy: "SELF" },
      facebook: {
        crosspost: [{ pageId: "1234", createPost: true }],
        donateCharityId: "999",
      },
    });

    try {
      await page.goto("/");
      // Each destination card carries a per-destination actions menu rather than
      // a bare edit button, so the name disambiguates without needing to reach
      // for the card element itself.
      await page.getByRole("button", { name: "Actions for e2e round trip" }).click();
      // Exact: the same menu carries "Edit routing", which is a link to another
      // page and not this dialog.
      await page.getByRole("menuitem", { name: "Edit", exact: true }).click();

      const dialog = page.getByRole("dialog");
      await expect(dialog).toBeVisible();
      // ONE field is changed, and it is the control that makes the rest of this
      // test mean anything.
      //
      // The first version saved the dialog untouched and asserted the blocks
      // survived. It passed with the load path mutated to drop them -- because
      // nothing proved the save had happened at all, so "the stored values are
      // still there" was true for the wrong reason. Renaming gives the
      // assertions below something that MUST change, so a save that silently
      // did not happen fails loudly instead of passing quietly.
      const nameField = dialog.getByLabel(/^name$/i);
      await nameField.fill("e2e round trip renamed");
      await dialog.getByRole("button", { name: /save/i }).click();
      await expect(dialog).toBeHidden();

      const { destination: after } = await api<DestinationEnvelope>(
        page,
        "GET",
        `/api/v1/destinations/${created.id}`,
      );

      // The control first: if this fails, the save never landed and every
      // assertion after it would have passed for the wrong reason.
      expect(
        after.name,
        "the rename did not persist, so this test never exercised a save at all",
      ).toBe("e2e round trip renamed");

      expect(
        after.compliance?.facebookPrivacy,
        "the audience the operator chose did not survive an unrelated save",
      ).toBe("SELF");
      expect(
        after.facebook?.donateCharityId,
        "the donate charity did not survive an unrelated save",
      ).toBe("999");
      expect(
        after.facebook?.crosspost?.[0]?.pageId,
        "the crosspost Page did not survive an unrelated save",
      ).toBe("1234");
      // createPost is the flag that decides whether a post is published as
      // somebody's Page, so losing it quietly is worse than losing the target.
      expect(
        after.facebook?.crosspost?.[0]?.createPost,
        "the crosspost create-post flag did not survive an unrelated save",
      ).toBe(true);
    } finally {
      await api(page, "DELETE", `/api/v1/destinations/${created.id}`);
    }
  });
});
