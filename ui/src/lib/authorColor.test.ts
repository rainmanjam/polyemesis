import { describe, expect, it } from "vitest";

import { authorInk } from "./authorColor";

/* A chat author's name is painted in the colour the PLATFORM chose for them
 * (Twitch's user colour, mostly). Platforms choose those for a dark chat
 * window: pure yellow and light green are common, and on the light theme's
 * white card they are about 1.1:1 -- the name disappears. The light theme
 * therefore darkens an author colour until it reads at AA on the darkest card
 * a chat row sits on, keeping its hue. The dark theme never touches it. */

const lin = (v: number) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
function lum(hex: string): number {
  const h = hex.length === 4 ? "#" + [...hex.slice(1)].map((c) => c + c).join("") : hex;
  const [r, g, b] = [1, 3, 5].map((i) => lin(parseInt(h.slice(i, i + 2), 16) / 255));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
// --card-raised in the light theme: the darkest ground a chat row is drawn on.
const GROUND = "#edf0f7";
const contrast = (fg: string) => {
  const [a, b] = [lum(fg), lum(GROUND)];
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
};

describe("authorInk", () => {
  it("leaves every colour alone on the dark theme", () => {
    for (const c of ["#ffff00", "#1e90ff", "#000000", "rebeccapurple", "not a colour"]) {
      expect(authorInk(c, "dark")).toBe(c);
    }
  });

  it.each(["#ffff00", "#00ff7f", "#ffd700", "#ff7f50", "#9acd32", "#00ffff", "#ffffff"])(
    "darkens %s until it reads at AA on the light theme's card",
    (c) => {
      expect(contrast(authorInk(c, "light"))).toBeGreaterThanOrEqual(4.5);
    },
  );

  it("keeps the hue: a yellow stays yellow, it does not turn grey", () => {
    const ink = authorInk("#ffff00", "light");
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(ink.slice(i, i + 2), 16));
    expect(Math.abs(r - g)).toBeLessThanOrEqual(2);
    expect(b).toBeLessThan(r / 2);
  });

  it("does not change a colour that already reads", () => {
    for (const c of ["#1e3a8a", "#8b0000", "#000000"]) expect(authorInk(c, "light")).toBe(c);
  });

  it("takes the three-digit form and any case", () => {
    expect(contrast(authorInk("#FF0", "light"))).toBeGreaterThanOrEqual(4.5);
  });

  it("hands back what it cannot parse, rather than inventing a colour", () => {
    for (const c of ["", "#12", "#gggggg", "#12345678", "rgb(1,2)", "hsl(x)"]) {
      expect(authorInk(c, "light")).toBe(c);
    }
  });
});

describe("authorInk with colours written other ways than hex", () => {
  it.each(["rgb(255, 255, 0)", "rgb(255 255 0)", "rgba(255,255,0,0.9)", "hsl(60, 100%, 50%)", "hsl(60 100% 50%)", "RGB(255,255,0)"])(
    "darkens %s, which is pure yellow",
    (c) => {
      const out = authorInk(c, "light");
      expect(out).toMatch(/^#[0-9a-f]{6}$/);
      expect(contrast(out)).toBeGreaterThanOrEqual(4.5);
    },
  );

  it("leaves a dark rgb() colour as it came", () => {
    expect(authorInk("rgb(30, 58, 138)", "light")).toBe("rgb(30, 58, 138)");
  });

  it("leaves these alone on the dark theme", () => {
    expect(authorInk("rgb(255,255,0)", "dark")).toBe("rgb(255,255,0)");
  });
});
