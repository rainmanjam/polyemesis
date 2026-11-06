// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useCopy } from "./useCopy";

function stubClipboard(writeText: ((s: string) => Promise<void>) | undefined) {
  Object.defineProperty(navigator, "clipboard", {
    value: writeText ? { writeText } : undefined,
    configurable: true,
  });
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.useRealTimers();
  stubClipboard(undefined);
});

describe("useCopy", () => {
  it("raises the copied flag after a copy and drops it after the delay", async () => {
    const writeText = vi.fn(async () => {});
    stubClipboard(writeText);
    const { result } = renderHook(() => useCopy(1500));
    expect(result.current.copied).toBe(false);
    await act(async () => {
      expect(await result.current.copy("abc")).toBe(true);
    });
    expect(writeText).toHaveBeenCalledWith("abc");
    expect(result.current.copied).toBe(true);
    act(() => vi.advanceTimersByTime(1500));
    expect(result.current.copied).toBe(false);
  });

  it("remembers WHICH item was copied, for a list of buttons", async () => {
    stubClipboard(async () => {});
    const { result } = renderHook(() => useCopy());
    await act(async () => void (await result.current.copy("x", "a.mp4")));
    expect(result.current.copiedKey).toBe("a.mp4");
  });

  it("reports failure instead of throwing when there is no clipboard (plain http)", async () => {
    stubClipboard(undefined);
    const onFail = vi.fn();
    const { result } = renderHook(() => useCopy(1500, onFail));
    await act(async () => {
      expect(await result.current.copy("abc")).toBe(false);
    });
    expect(result.current.copied).toBe(false);
    expect(onFail).toHaveBeenCalledTimes(1);
  });

  it("reports failure when the browser refuses the write", async () => {
    stubClipboard(async () => {
      throw new Error("denied");
    });
    const onFail = vi.fn();
    const { result } = renderHook(() => useCopy(1500, onFail));
    await act(async () => {
      expect(await result.current.copy("abc")).toBe(false);
    });
    expect(onFail).toHaveBeenCalledTimes(1);
  });
});
