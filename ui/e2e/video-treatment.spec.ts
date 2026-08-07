import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   How a destination's video treatment is chosen.

   The redesign in docs/notes/video-treatment-ui.md replaced a single <Select>
   listing "passthrough" alongside every encode with two radio cards, on the
   argument that copying video is nearly free and a shared encode is the most
   expensive thing on the page — so they are not the same kind of choice and a
   dropdown was wrong to say they were.

   What is asserted here is the part unit tests cannot reach: that the cards are
   actually wired to the picker, that the picker COLLAPSES under Copy, and that
   the consequence line is fed the real usage numbers from the server rather
   than a plausible-looking constant. The phrasing and the arithmetic are
   covered exhaustively (and mutation-tested) in
   src/lib/rendition-consequence.test.ts; duplicating that here would be slow
   and would not add a guard.

   The fourth consequence state — "N other enabled destinations stay on this
   encode" — is the reason this file exists. It needs two destinations sharing
   one encode, which is a fixture no unit test can stand up, and it shipped
   without anyone confirming N was right.
   =========================================================================== */

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

/** Same in-page fetch the rest of the suite uses, so the call carries the
 *  session cookie and the CSRF token. */
async function api<T>(
  page: Page,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
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
      if (!res.ok)
        throw new Error(
          `${m} ${p} failed (${res.status}): ${await res.text()}`,
        );
      return res.status === 204 ? null : await res.json();
    },
    { m: method, p: path, b: body },
  );
}

interface Rendition {
  id: number;
  name: string;
}
interface Destination {
  id: number;
  name: string;
  renditionId?: number | null;
}

async function makeRendition(page: Page, name: string): Promise<Rendition> {
  const { rendition } = await api<{ rendition: Rendition }>(
    page,
    "POST",
    "/api/v1/renditions",
    {
      name,
      width: 1920,
      height: 1080,
      fps: 60,
      videoBitrate: 6000,
      encoder: "libx264",
      preset: "veryfast",
      gopSeconds: 2,
      overlay: {},
      text: {},
      note: "",
    },
  );
  return rendition;
}

async function makeDestination(
  page: Page,
  name: string,
  extra: Record<string, unknown> = {},
): Promise<Destination> {
  const { destination } = await api<{ destination: Destination }>(
    page,
    "POST",
    "/api/v1/destinations",
    {
      name,
      kind: "rtmp",
      platform: "custom",
      url: "rtmp://example.invalid/live/",
      streamKey: "e2e-key",
      audioBitrate: 160,
      ...extra,
    },
  );
  return destination;
}

/** Opens the edit dialog for a destination by name, the way an operator does. */
async function openEditor(page: Page, name: string) {
  await page.goto("/");
  await page.getByRole("button", { name: `Actions for ${name}` }).click();
  // Exact: the same menu carries "Edit routing", a link to a different page.
  await page.getByRole("menuitem", { name: "Edit", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  return dialog;
}

/** The encode picker, by test id.
 *
 *  Not getByRole("combobox"): the dialog carries several, and .first() picked
 *  the platform selector — so the option click below waited on a menu that had
 *  never opened and the test died on a 30s timeout rather than an assertion. */
function picker(dialog: ReturnType<Page["getByRole"]>) {
  return dialog.getByTestId("rendition-picker");
}

/** Choose a named encode explicitly.
 *
 *  Clicking the "shared encode" card seeds the FIRST rendition in the list,
 *  which is whichever one the database hands back first — not necessarily the
 *  one this test created. Asserting on the seed is how "the chosen encode is
 *  what gets saved" failed with `Expected: 6, Received: 2` while the feature
 *  was working perfectly. */
async function chooseEncode(
  page: Page,
  dialog: ReturnType<Page["getByRole"]>,
  name: string,
) {
  await picker(dialog).click();
  await page.getByRole("option", { name: new RegExp(name) }).click();
}

test.describe("choosing a video treatment", () => {
  test("the encode picker is collapsed under Copy and revealed by the encode card", async ({
    page,
  }) => {
    await signIn(page);
    const rendition = await makeRendition(page, "e2e collapse tier");
    const dest = await makeDestination(page, "e2e collapse");

    try {
      const dialog = await openEditor(page, "e2e collapse");

      // The group exists and is named. role="radio" outside a radiogroup is
      // announced as an isolated control with no alternatives, which is why
      // this is asserted rather than assumed.
      const group = dialog.getByRole("radiogroup", { name: "Video treatment" });
      await expect(group).toBeVisible();

      const copy = group.getByRole("radio", { name: /Copy the source video/ });
      const shared = group.getByRole("radio", {
        name: /Use a shared video encode/,
      });

      // A new destination has no rendition, so Copy is the state.
      await expect(copy).toHaveAttribute("aria-checked", "true");
      await expect(shared).toHaveAttribute("aria-checked", "false");

      // THE PROGRESSIVE DISCLOSURE: the free path has nothing to configure, so
      // nothing about encodes is on screen at all. If the picker rendered here
      // it would imply Copy has settings.
      await expect(picker(dialog)).toHaveCount(0);

      await shared.click();
      await expect(shared).toHaveAttribute("aria-checked", "true");
      await expect(copy).toHaveAttribute("aria-checked", "false");

      // Revealed, and seeded with a real encode rather than left empty — an
      // empty required picker behind a radio the operator just chose is a dead
      // end they have to discover by saving.
      await expect(picker(dialog)).toBeVisible();
      await expect(picker(dialog)).toContainText("1920×1080");

      // And back: the picker collapses again.
      await copy.click();
      await expect(picker(dialog)).toHaveCount(0);
    } finally {
      await api(page, "DELETE", `/api/v1/destinations/${dest.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${rendition.id}`);
    }
  });

  test("the join consequence reports whether an encode is already running", async ({
    page,
  }) => {
    await signIn(page);
    const idle = await makeRendition(page, "e2e idle tier");
    const busy = await makeRendition(page, "e2e busy tier");

    // One ENABLED destination on `busy`, so it is genuinely encoding. Without
    // this the two branches are indistinguishable and the line could be a
    // constant.
    const holder = await makeDestination(page, "e2e holder", {
      renditionId: busy.id,
      enabled: true,
    });
    const dest = await makeDestination(page, "e2e joiner");

    try {
      const dialog = await openEditor(page, "e2e joiner");
      await dialog
        .getByRole("radio", { name: /Use a shared video encode/ })
        .click();

      // Pick the idle tier: joining it will START an encode. This is the
      // expensive branch and the one worth naming.
      await chooseEncode(page, dialog, "e2e idle tier");
      await expect(
        dialog.getByText(
          "Starts one shared encode when an enabled destination uses it.",
        ),
      ).toBeVisible();

      // Pick the busy tier: joining costs nothing.
      await chooseEncode(page, dialog, "e2e busy tier");
      await expect(dialog.getByText(/already encoding/)).toBeVisible();
      await expect(dialog.getByText(/no new encode starts/)).toBeVisible();
    } finally {
      await api(page, "DELETE", `/api/v1/destinations/${dest.id}`);
      await api(page, "DELETE", `/api/v1/destinations/${holder.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${idle.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${busy.id}`);
    }
  });

  test("leaving an encode says whether it stops or keeps running for others", async ({
    page,
  }) => {
    await signIn(page);
    const shared = await makeRendition(page, "e2e shared tier");
    const solo = await makeRendition(page, "e2e solo tier");

    // Two enabled destinations on `shared`, one on `solo`. That is the whole
    // point: the same action produces opposite consequences depending on who
    // else is on the encode, and nothing surveyed in the field tells an
    // operator either one.
    const a = await makeDestination(page, "e2e leaver a", {
      renditionId: shared.id,
      enabled: true,
    });
    const b = await makeDestination(page, "e2e leaver b", {
      renditionId: shared.id,
      enabled: true,
    });
    const lone = await makeDestination(page, "e2e lone", {
      renditionId: solo.id,
      enabled: true,
    });

    try {
      // ---- others remain ----
      let dialog = await openEditor(page, "e2e leaver a");
      await dialog
        .getByRole("radio", { name: /Copy the source video/ })
        .click();
      // Exactly one other enabled destination is on it, so the encode survives.
      await expect(
        dialog.getByText(
          "1 other enabled destination stay on “e2e shared tier”. Nothing else changes.",
        ),
      ).toBeVisible();
      await expect(dialog.getByText(/Stops the/)).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(dialog).toBeHidden();

      // ---- last one out ----
      dialog = await openEditor(page, "e2e lone");
      await dialog
        .getByRole("radio", { name: /Copy the source video/ })
        .click();
      await expect(
        dialog.getByText(
          "Stops the “e2e solo tier” encode — no other enabled destination is on it.",
        ),
      ).toBeVisible();
    } finally {
      for (const d of [a, b, lone])
        await api(page, "DELETE", `/api/v1/destinations/${d.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${shared.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${solo.id}`);
    }
  });

  test("the chosen encode is what gets saved", async ({ page }) => {
    // The cards can be wired to the picker and the picker to nothing. This is
    // the end of the chain: what the operator selected is what the server
    // stores, and what comes back when they reopen.
    await signIn(page);
    const rendition = await makeRendition(page, "e2e persisted tier");
    const dest = await makeDestination(page, "e2e persist");

    try {
      const dialog = await openEditor(page, "e2e persist");
      await dialog
        .getByRole("radio", { name: /Use a shared video encode/ })
        .click();
      await chooseEncode(page, dialog, "e2e persisted tier");
      await dialog.getByRole("button", { name: /save/i }).click();
      await expect(dialog).toBeHidden();

      const { destination: after } = await api<{ destination: Destination }>(
        page,
        "GET",
        `/api/v1/destinations/${dest.id}`,
      );
      expect(after.renditionId).toBe(rendition.id);

      // And it round-trips back into the dialog rather than reopening on Copy,
      // which would quietly discard the choice on the next unrelated edit.
      const reopened = await openEditor(page, "e2e persist");
      await expect(
        reopened.getByRole("radio", { name: /Use a shared video encode/ }),
      ).toHaveAttribute("aria-checked", "true");
    } finally {
      await api(page, "DELETE", `/api/v1/destinations/${dest.id}`);
      await api(page, "DELETE", `/api/v1/renditions/${rendition.id}`);
    }
  });
});
