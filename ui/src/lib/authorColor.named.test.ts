// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { authorInk } from "./authorColor";

/* A named colour ("gold") can only be resolved by the browser. authorInk asks a
 * 1x1 canvas, which reports any CSS colour it accepts as #rrggbb. jsdom has no
 * canvas, so one is stood in that knows two names. */

function stubCanvas(table: Record<string, string>) {
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(function () {
    let fill = "#000000";
    return {
      get fillStyle() {
        return fill;
      },
      set fillStyle(v: string) {
        // A real canvas ignores a value it cannot parse and keeps the last one.
        if (v.startsWith("#")) fill = v;
        else if (table[v]) fill = table[v];
      },
    } as unknown as CanvasRenderingContext2D;
  });
}
afterEach(() => vi.restoreAllMocks());

describe("authorInk with a named colour", () => {
  it("darkens a pale name once the browser has said what it is", () => {
    stubCanvas({ gold: "#ffd700" });
    const out = authorInk("gold", "light");
    expect(out).toMatch(/^#[0-9a-f]{6}$/);
    expect(out).not.toBe("gold");
  });

  it("returns a name the browser does not know, unchanged", () => {
    stubCanvas({});
    expect(authorInk("notacolour", "light")).toBe("notacolour");
  });

  it("returns a name unchanged where there is no canvas at all", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
    expect(authorInk("gold", "light")).toBe("gold");
  });
});
