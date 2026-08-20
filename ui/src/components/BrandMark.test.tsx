import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { BrandMark } from "./BrandMark";

/* THE PRODUCT HAS ONE LOGO, AND SOMETHING HAS TO SAY SO.
 *
 * Before this the app wore three marks -- the site's five bars, lucide's
 * generic <AudioLines> in the header and sign-in screen, and a four-bar
 * single-colour favicon on a different background. Nothing asserted anything
 * about any of them, which is exactly how three variants coexisted unnoticed
 * until someone put two tabs side by side.
 *
 * So this compares the rendered component against the FILES the other surfaces
 * ship, rather than against a copy of itself. A snapshot would pass just as
 * happily on three different logos as on one.
 */

const rects = (svg: string) =>
  [...svg.matchAll(/<rect\b[^>]*>/g)]
    .map((m) => m[0])
    .map((tag) => ({
      x: /\bx="([^"]+)"/.exec(tag)?.[1],
      y: /\by="([^"]+)"/.exec(tag)?.[1],
      w: /\bwidth="([^"]+)"/.exec(tag)?.[1],
      h: /\bheight="([^"]+)"/.exec(tag)?.[1],
      fill: /\bfill="([^"]+)"/.exec(tag)?.[1],
    }));

describe("BrandMark", () => {
  const rendered = renderToStaticMarkup(<BrandMark />);

  it("draws the five bars, in the three brand colours", () => {
    const bars = rects(rendered);
    expect(bars).toHaveLength(5);
    // Unequal heights are the whole idea -- ingest tracks at different levels.
    // Five identical bars would render as a barcode and mean nothing.
    expect(new Set(bars.map((b) => b.h)).size).toBeGreaterThan(1);
    expect(bars.map((b) => b.fill)).toEqual([
      "#5b7fc7",
      "#5b7fc7",
      "#3ecf6d",
      "#3ecf6d",
      "#45b5d0",
    ]);
  });

  /* THE ONE THAT WOULD HAVE CAUGHT THE ORIGINAL BUG, in two halves.
   *
   * First: the app's tab icon and the website's are the same file. That is the
   * comparison the old four-bar favicon would have failed, and it is checked as
   * bytes because anything looser is a check that can drift.
   *
   * Second: the component carries the same colours. NOT the same coordinates --
   * the favicon is deliberately inset to sit inside its rounded tile (x starts
   * at 2.5 rather than 1, and the tallest bar is 17 rather than 18), which is
   * layout for a different container rather than a different mark. Asserting
   * coordinate equality would fail on a difference that is correct, and the
   * first version of this test did exactly that.
   */
  it("ships the same favicon as the website", () => {
    const appIcon = readFileSync(new URL("../../public/favicon.svg", import.meta.url), "utf8");
    const siteIcon = readFileSync(
      new URL("../../../web/public/favicon.svg", import.meta.url),
      "utf8",
    );
    // The app's copy carries a comment explaining its history; the drawing is
    // what has to match, so compare the markup with comments stripped.
    const strip = (s: string) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/\s+/g, " ").trim();
    expect(strip(appIcon)).toBe(strip(siteIcon));
  });

  it("uses the same colours as the favicon, which is the mark's identity", () => {
    const favicon = readFileSync(new URL("../../public/favicon.svg", import.meta.url), "utf8");
    // The favicon's first rect is its background tile; the bars are the rest.
    const bars = rects(favicon).slice(1);
    expect(bars.map((b) => b.fill)).toEqual(rects(rendered).map((b) => b.fill));
    expect(bars).toHaveLength(rects(rendered).length);
  });

  it("scales without distorting, because a viewBox is what makes that true", () => {
    expect(rendered).toContain('viewBox="0 0 22 22"');
    const small = renderToStaticMarkup(<BrandMark size={12} />);
    expect(small).toContain('width="12"');
    expect(small).toContain('viewBox="0 0 22 22"');
  });

  it("is hidden from assistive technology, because the wordmark beside it is the name", () => {
    expect(rendered).toContain('aria-hidden="true"');
  });
});
