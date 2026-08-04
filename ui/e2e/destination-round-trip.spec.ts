import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   The destination editor must give back everything it was given.

   This exists because of a mutation that survived every guard on the branch
   that added Facebook's create-time settings: changing
   `setFacebook(destination.facebook ?? {})` to `setFacebook({})` left the whole
   suite green. An operator would then open a configured Facebook destination,
   save it without touching anything, and silently lose their crossposting and
   donate settings.

   Every guard that covers this area is a STRING-PRESENCE check on the source --
   does the payload literal mention `facebook`, does the dialog offer a
   `<SelectItem value="SELF">`. Those prove a field is wired and a control is
   reachable. They cannot see a load path that drops what it read, because
   nothing about the source text changes shape when it does.

   So this asserts the only thing that actually matters: configure a
   destination, open the editor, save it unchanged, and read it back. The
   pre-existing compliance block rides along in the same assertion, because it
   has the identical blind spot and there is no reason to prove this for one
   JSON column and not its neighbour.
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
      // Nothing is changed. A save that only ever preserves values the operator
      // just retyped is not preserving anything.
      await dialog.getByRole("button", { name: /save/i }).click();
      await expect(dialog).toBeHidden();

      const { destination: after } = await api<DestinationEnvelope>(
        page,
        "GET",
        `/api/v1/destinations/${created.id}`,
      );

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
