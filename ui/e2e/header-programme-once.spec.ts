import { expect, test, type Page } from "@playwright/test";

/* ON A PHONE THE HEADER NAMES THE PROGRAMME ONCE (2026-11 review).
 *
 * Below sm, a multi-programme header printed the programme twice: in the
 * on-air chip beside the status dot ("● Main Pr…") and in the programme
 * switcher beside it. The switcher is the one that can be acted on, so it
 * keeps the name; the chip keeps its dot and its state words, and carries
 * the programme for screen readers only.
 *
 * The suite runs against a one-source install, where there is no switcher at
 * all, so the source list is answered with that source plus a second one. */

const PROGRAMME = "Layout Check Programme";

async function twoProgrammes(page: Page) {
  await page.route(/\/api\/v1\/sources$/, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const rows = (await res.json()) as Record<string, unknown>[];
    const first = { ...rows[0], name: `${PROGRAMME} — Studio A` };
    const second = { ...rows[0], id: Number(rows[0].id) + 1000, name: "Second Programme — Studio B", isDefault: false };
    await route.fulfill({ response: res, json: [first, second, ...rows.slice(1)] });
  });
}

/** Visible text in the header that names the programme, one entry per element. */
async function namesInHeader(page: Page): Promise<string[]> {
  return page.$$eval("header *", (els, name) => {
    const out: string[] = [];
    for (const el of els as HTMLElement[]) {
      const own = Array.from(el.childNodes)
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => n.textContent ?? "")
        .join("");
      // The truncated chip read "Main Pr…": a prefix of the name counts.
      const shown = own.replace(/…$/, "").trim();
      if (shown.length < 4 || !(name as string).startsWith(shown.slice(0, Math.min(shown.length, (name as string).length)))) continue;
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      const hidden =
        r.width < 2 || r.height < 2 || cs.visibility === "hidden" || cs.display === "none" ||
        // sr-only: clipped to a 1px box.
        (r.width <= 1 && r.height <= 1) || cs.clip === "rect(0px, 0px, 0px, 0px)";
      if (!hidden) out.push(shown);
    }
    return out;
  }, PROGRAMME);
}

for (const width of [360, 390]) {
  test(`at ${width}px the header shows the programme name once, in the switcher`, async ({ page }) => {
    await twoProgrammes(page);
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/");
    const switcher = page.getByTestId("programme-switcher");
    await expect(switcher).toBeVisible();
    await expect(switcher).toContainText(PROGRAMME);

    const shown = await namesInHeader(page);
    expect(shown, `programme name visible in the header at ${width}px`).toHaveLength(1);

    // The on-air chip keeps its dot and still names the programme for a
    // screen reader.
    const chip = page.getByTestId("mobile-on-air");
    await expect(chip).toBeVisible();
    await expect(chip).toContainText(PROGRAMME);
    // ...and only for a screen reader. Checked on the markup as well as on
    // what is visible: at 360px a chip printing the name is squeezed to its
    // dot, so the measurement above alone would let the duplicate back in.
    const printed = await chip.evaluate((el) =>
      Array.from(el.children)
        .filter((c) => !c.classList.contains("sr-only"))
        .map((c) => c.textContent ?? "")
        .join(" "),
    );
    expect(printed).not.toContain(PROGRAMME);
    // The dot survives the squeeze.
    const dot = await chip.evaluate((el) => (el.firstElementChild as HTMLElement).getBoundingClientRect().width);
    expect(dot).toBeGreaterThan(0);
  });
}

test("at 1440px the desktop header is unchanged: the switcher names the programme", async ({ page }) => {
  await twoProgrammes(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.getByTestId("programme-switcher")).toContainText(`${PROGRAMME} — Studio A`);
  await expect(page.getByTestId("mobile-on-air")).toBeHidden();
});
