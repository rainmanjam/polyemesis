import { expect, test } from "@playwright/test";

/* ===========================================================================
   Shell layout invariants.

   The app shell is a fixed-height box: `h-dvh` on the root, a `shrink-0`
   header, then a row holding the nav and the main region. Exactly ONE thing in
   that box is supposed to scroll -- <main>. Anything else that overflows is a
   layout bug, and it shows up to the user as a second scrollbar beside the
   first rather than as anything obviously broken.
   =========================================================================== */

/** Short enough that the 14 nav links plus the collapse toggle cannot fit. */
const SHORT = { width: 936, height: 500 };
/** Tall enough that the nav fits with room to spare -- the control case. */
const TALL = { width: 1440, height: 900 };

async function metrics(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const b = document.body;
    const nav = document.querySelector("nav")!;
    const main = document.querySelector("main")!;
    return {
      bodyOverflowPx: b.scrollHeight - b.clientHeight,
      navOverflowY: getComputedStyle(nav).overflowY,
      navScrolls: nav.scrollHeight > nav.clientHeight + 1,
      mainScrolls: main.scrollHeight > main.clientHeight + 1,
      // Anything unclipped hanging below the fold is what extends the document.
      spillBelowFold: [...document.querySelectorAll("body *")].filter((el) => {
        const r = el.getBoundingClientRect();
        if (!r.height && !r.width) return false;
        if (r.bottom <= document.documentElement.clientHeight + 1) return false;
        for (let p = el.parentElement; p && p !== b; p = p.parentElement) {
          const cs = getComputedStyle(p);
          if (cs.overflowY !== "visible" || cs.overflowX !== "visible") return false;
        }
        return true;
      }).length,
    };
  });
}

test.describe("app shell scrolling", () => {
  test("a short viewport does not produce a second, page-level scrollbar", async ({ page }) => {
    await page.setViewportSize(SHORT);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();

    const m = await metrics(page);

    // The bug: the nav's content (links + the mt-auto collapse toggle) spilled
    // past the h-dvh box because <nav> had no overflow handling, extending the
    // document by the height of whatever hung out the bottom. The browser then
    // drew a page scrollbar immediately beside <main>'s -- two bars, one of
    // which scrolled nothing the user cared about.
    expect(m.spillBelowFold, "elements hanging unclipped below the fold").toBe(0);
    expect(m.bodyOverflowPx, "the document must not extend past the viewport").toBe(0);
  });

  test("the nav scrolls itself when it cannot fit, rather than the page", async ({ page }) => {
    await page.setViewportSize(SHORT);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();

    const m = await metrics(page);
    // A sidebar too tall for the viewport has to scroll somewhere. The point is
    // that it scrolls INSIDE its own box, which is one scrollbar in the place
    // it belongs, not a second one on the document.
    expect(m.navOverflowY, "nav must own its overflow").not.toBe("visible");
    expect(m.bodyOverflowPx).toBe(0);
  });

  test("main is still the scrolling region for page content", async ({ page }) => {
    await page.setViewportSize(SHORT);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();

    // Guards against "fixing" the double scrollbar by clipping everything: the
    // dashboard is taller than the fold and must remain reachable.
    expect((await metrics(page)).mainScrolls).toBe(true);
  });

  test("a tall viewport needs no nav scrollbar at all", async ({ page }) => {
    await page.setViewportSize(TALL);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();

    const m = await metrics(page);
    expect(m.navScrolls, "nav should not scroll when it fits").toBe(false);
    expect(m.bodyOverflowPx).toBe(0);
    expect(m.spillBelowFold).toBe(0);
  });
});
