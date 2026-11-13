import { describe, expect, it, vi } from "vitest";
import { scrollToPosition, type Scrollable } from "./scroll";

describe("scrollToPosition", () => {
  it("asks for smooth or instant scrolling", () => {
    const scrollTo = vi.fn();
    scrollToPosition({ scrollTo } as Scrollable, { left: 3, top: 400 }, true);
    expect(scrollTo).toHaveBeenCalledWith({ left: 3, top: 400, behavior: "smooth" });
    scrollToPosition({ scrollTo } as Scrollable, { left: 3, top: 400 }, false);
    expect(scrollTo).toHaveBeenLastCalledWith({ left: 3, top: 400, behavior: "instant" });
  });

  it("falls back to scrollTo(x, y) on an engine that rejects behavior: instant", () => {
    const scrollTo = vi.fn((a: unknown, b?: unknown) => {
      if (typeof a === "object" && (a as ScrollToOptions).behavior === "instant") throw new TypeError("not a valid value");
      return b;
    });
    expect(() => scrollToPosition({ scrollTo, scrollX: 7 } as unknown as Scrollable, { top: 250 }, false)).not.toThrow();
    expect(scrollTo).toHaveBeenLastCalledWith(7, 250);
  });
});
