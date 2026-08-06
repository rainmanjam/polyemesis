import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   Translation, end to end.

   The catalogue tests in src/lib/i18n.test.ts prove the JSON is well formed —
   no stray keys, no corrupted placeholders, no leaked English. They cannot
   prove the page actually READS from it. A component holding a hard-coded
   literal passes every one of those checks and still renders English to a
   German operator, which is the exact defect this file exists to catch.

   So these assert on rendered text after switching language, and they assert
   on strings that only appear if the catalogue was consulted.
   =========================================================================== */

const LANGUAGE_KEY = "polyemesis.language";

/** Sets the stored language before the app boots, which is how lib/i18n.ts
 *  picks it up — switching through the UI would work too but couples every
 *  assertion here to the language menu's markup. */
async function signInAs(page: Page, lang: string) {
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    [LANGUAGE_KEY, lang] as const,
  );
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

test.describe("page translation", () => {
  test("the sources page renders in German", async ({ page }) => {
    await signInAs(page, "de");
    await page.goto("/sources");

    await expect(page.getByRole("heading", { name: "Quellen" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Quelle hinzufügen" })).toBeVisible();
    // The subtitle is a full sentence, so this also catches a catalogue wired
    // up for labels only.
    await expect(page.getByText(/Je ein eingehendes Programm/)).toBeVisible();
  });

  test("the same page renders in Japanese", async ({ page }) => {
    // A second script proves the switch is not accidentally hard-coded to one
    // locale, and CJK exercises the layout at a very different text length.
    await signInAs(page, "ja");
    await page.goto("/sources");

    await expect(page.getByRole("heading", { name: "ソース" })).toBeVisible();
    await expect(page.getByRole("button", { name: "ソースを追加" })).toBeVisible();
  });

  test("English remains the fallback", async ({ page }) => {
    await signInAs(page, "en");
    await page.goto("/sources");
    await expect(page.getByRole("heading", { name: "Sources" })).toBeVisible();
  });

  test("<html lang> follows the catalogue", async ({ page }) => {
    // Screen readers take pronunciation from this. A German UI announced with
    // an English voice is barely more usable than the untranslated page.
    await signInAs(page, "de");
    await page.goto("/sources");
    await expect(page.locator("html")).toHaveAttribute("lang", "de");
  });
});

test.describe("setting help", () => {
  test("an info icon opens an explanation of the setting", async ({ page }) => {
    await signInAs(page, "en");
    await page.goto("/sources");

    // Click rather than hover: the whole reason this is a popover is that a
    // hover tooltip is unreachable on touch and awkward under a screen reader.
    const hint = page.getByRole("button", { name: /Ingest — More information/ }).first();
    await hint.click();

    await expect(page.getByText(/How your encoder reaches this source/)).toBeVisible();
    // The genuine domain knowledge is the point of the feature, not a restated
    // label: this explains what SRT actually does differently.
    await expect(page.getByText(/retransmitting what went missing/)).toBeVisible();
  });

  test("the explanation is translated too", async ({ page }) => {
    await signInAs(page, "de");
    await page.goto("/sources");

    // The requirement was help "in their language" — a translated label with an
    // English paragraph behind the icon would miss the point entirely.
    await page.getByRole("button", { name: /Eingang — Weitere Informationen/ }).first().click();
    await expect(page.getByText(/Wie Ihr Encoder diese Quelle erreicht/)).toBeVisible();
  });

  test("the hint is reachable from the keyboard", async ({ page }) => {
    await signInAs(page, "en");
    await page.goto("/sources");

    const hint = page.getByRole("button", { name: /Ingest — More information/ }).first();
    await hint.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/How your encoder reaches this source/)).toBeVisible();

    // And dismissible without a pointer.
    await page.keyboard.press("Escape");
    await expect(page.getByText(/How your encoder reaches this source/)).toBeHidden();
  });
});
