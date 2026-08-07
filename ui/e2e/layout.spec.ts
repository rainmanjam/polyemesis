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
    const de = document.documentElement;
    return {
      // documentElement, NOT body.
      //
      // This measured body and could not see the bug it was written for. An
      // absolutely positioned element with no positioned ancestor resolves
      // against the initial containing block -- the html element -- so it
      // extends documentElement.scrollHeight while body stays exactly the
      // viewport height. The Dashboard's aria-live <output class="sr-only">
      // did this: body measured a clean 900/900 and this assertion passed
      // while the user was looking at two scrollbars.
      docOverflowPx: de.scrollHeight - de.clientHeight,
      bodyOverflowPx: b.scrollHeight - b.clientHeight,

      // Absolutely positioned elements whose containing block is the ICB and
      // which sit below the fold. spillBelowFold cannot catch these: it bails
      // as soon as an ancestor scrolls, which is right for ordinary content
      // (that content is legitimately inside the scroller) and wrong for these
      // (position:absolute lets them escape the ancestor that clips).
      escapedAbsolutes: [...document.querySelectorAll("body *")]
        .filter((el) => {
          const cs = getComputedStyle(el);
          if (cs.position !== "absolute") return false;
          for (let q = el.parentElement; q; q = q.parentElement) {
            if (getComputedStyle(q).position !== "static") return false; // contained
          }
          return el.getBoundingClientRect().bottom > de.clientHeight + 1;
        })
        .map((el) => `${el.tagName.toLowerCase()}.${(el.className || "").toString().split(" ")[0]}`),

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
    expect(m.escapedAbsolutes, "absolutely positioned elements escaping to the html element").toEqual([]);
    expect(m.docOverflowPx, "the document must not extend past the viewport").toBe(0);
    expect(m.bodyOverflowPx, "body must not extend past the viewport").toBe(0);
  });

  test("no page-level scrollbar on a tall viewport either", async ({ page }) => {
    // The escaping <output> was viewport-independent: it sat at a fixed offset
    // inside main's content, so it extended the document by the same amount at
    // 900px tall as at 500px. Every case in this file used a SHORT viewport,
    // because the original bug was the nav not fitting -- so the whole suite
    // could pass with a second scrollbar present at ordinary window sizes.
    await page.setViewportSize(TALL);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();

    const m = await metrics(page);
    expect(m.escapedAbsolutes, "absolutely positioned elements escaping to the html element").toEqual([]);
    expect(m.docOverflowPx, "the document must not extend past the viewport").toBe(0);
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
