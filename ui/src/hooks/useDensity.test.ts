// @vitest-environment jsdom

/* Density is a remembered preference that writes to <html>, and both halves of
   that had no test: whether the attribute index.css selects on is actually set,
   and whether a browser that refuses localStorage takes the console down with
   it. Safari in a private window throws on ANY access — the same hazard, and
   the same guard, as hooks/useNavCollapsed.ts. */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDensity } from "./useDensity";

const KEY = "polyemesis.density";

describe("useDensity", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    delete document.documentElement.dataset.density;
  });
  afterEach(() => vi.restoreAllMocks());

  it("starts comfortable when nothing is stored", () => {
    const { result } = renderHook(() => useDensity());
    expect(result.current[0]).toBe("comfortable");
  });

  it("restores compact across a reload", () => {
    // A density that resets on reload is a setting nobody uses twice.
    window.localStorage.setItem(KEY, "compact");
    const { result } = renderHook(() => useDensity());
    expect(result.current[0]).toBe("compact");
  });

  it("treats an unrecognised stored value as comfortable", () => {
    window.localStorage.setItem(KEY, "spacious");
    const { result } = renderHook(() => useDensity());
    expect(result.current[0]).toBe("comfortable");
  });

  it("writes the ATTRIBUTE index.css selects on, not a class", () => {
    // The whole feature is rescaling one Tailwind variable through this
    // attribute. If it stops being set, every page silently keeps comfortable
    // spacing and the toggle appears to do nothing at all.
    const { result } = renderHook(() => useDensity());
    expect(document.documentElement.dataset.density).toBe("comfortable");
    act(() => result.current[1]());
    expect(document.documentElement.dataset.density).toBe("compact");
  });

  it("toggles back and forth, and persists each way", () => {
    const { result } = renderHook(() => useDensity());
    act(() => result.current[1]());
    expect(result.current[0]).toBe("compact");
    expect(window.localStorage.getItem(KEY)).toBe("compact");
    act(() => result.current[1]());
    expect(result.current[0]).toBe("comfortable");
    expect(window.localStorage.getItem(KEY)).toBe("comfortable");
  });

  it("still renders when the browser refuses storage entirely", () => {
    // Safari, private window. The preference does not survive the session --
    // documented and acceptable. Taking the console down would not be.
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("The operation is insecure.");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("The operation is insecure.");
    });
    const { result } = renderHook(() => useDensity());
    expect(result.current[0]).toBe("comfortable");
    expect(() => act(() => result.current[1]())).not.toThrow();
    expect(result.current[0]).toBe("compact");
    expect(document.documentElement.dataset.density).toBe("compact");
  });
});
