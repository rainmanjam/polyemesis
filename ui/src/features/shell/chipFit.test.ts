// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { chipFits, naturalWidth } from "./chipFit";

/* jsdom lays nothing out, so the geometry is stubbed per element. The real
 * layout is measured by e2e/header-programme-once.spec.ts; these pin the
 * arithmetic: what counts as "room", and what an item's natural width is. */

function el(tag: string, box: { offset?: number; client?: number; scroll?: number } = {}, style = ""): HTMLElement {
  const e = document.createElement(tag);
  if (style) e.setAttribute("style", style);
  const offset = box.offset ?? 0;
  Object.defineProperty(e, "offsetWidth", { configurable: true, get: () => offset });
  Object.defineProperty(e, "clientWidth", { configurable: true, get: () => box.client ?? offset });
  Object.defineProperty(e, "scrollWidth", { configurable: true, get: () => box.scroll ?? box.client ?? offset });
  return e;
}

/** A row of `width` holding a chip (6px dot, 6px gap) and `others` items. */
function row(width: number, others: HTMLElement[], words: number, gap = 8) {
  const r = el("div", { offset: width, client: width }, `column-gap: ${gap}px; padding-left: 12px; padding-right: 12px`);
  const chip = el("span", { offset: 0 }, "column-gap: 6px");
  chip.append(el("span", { offset: 6 }));
  r.append(chip, ...others);
  document.body.append(r);
  return { chip, measure: el("span", { offset: words }) };
}

describe("naturalWidth", () => {
  it("is the width an item occupies when nothing in it is hidden", () => {
    expect(naturalWidth(el("div", { offset: 120 }))).toBe(120);
  });

  it("adds back what an ellipsis inside it is hiding", () => {
    const sw = el("button", { offset: 100 });
    sw.append(el("span", { offset: 80, client: 80, scroll: 130 }));
    expect(naturalWidth(sw)).toBe(150);
  });

  it("never exceeds the item's own max-width", () => {
    const sw = el("button", { offset: 100 }, "max-width: 120px");
    sw.append(el("span", { offset: 80, client: 80, scroll: 200 }));
    expect(naturalWidth(sw)).toBe(120);
  });
});

describe("chipFits", () => {
  // Row 360: content 336. Others 28 + 18 + 150 + 56 = 252, plus 4 gaps of 8 =
  // 284. Room 52. The chip needs dot 6 + gap 6 + words.
  const others = () => [el("button", { offset: 28 }), el("div", { offset: 18 }), el("button", { offset: 150 }), el("div", { offset: 56 })];

  it("fits when dot, gap and words are within the room", () => {
    const { chip, measure } = row(360, others(), 40);
    expect(chipFits(chip, measure)).toBe(true);
  });

  it("does not fit when the words are one pixel too wide", () => {
    const { chip, measure } = row(360, others(), 41);
    expect(chipFits(chip, measure)).toBe(false);
  });

  it("counts a squeezed switcher at the width its whole label wants", () => {
    const squeezed = el("button", { offset: 110 });
    squeezed.append(el("span", { offset: 90, client: 90, scroll: 130 }));
    const items = [el("button", { offset: 28 }), el("div", { offset: 18 }), squeezed, el("div", { offset: 56 })];
    // At its squeezed 110 the room would be 92 and 40px of words would fit;
    // at its natural 150 the room is 52, and 41px does not.
    const { chip, measure } = row(360, items, 41);
    expect(chipFits(chip, measure)).toBe(false);
  });

  it("ignores items that are not on screen", () => {
    const { chip, measure } = row(360, [...others(), el("div", { offset: 0 })], 40);
    expect(chipFits(chip, measure)).toBe(true);
  });

  it("says fits when the chip has no row to measure against", () => {
    const chip = el("span");
    expect(chipFits(chip, el("span", { offset: 999 }))).toBe(true);
  });
});
