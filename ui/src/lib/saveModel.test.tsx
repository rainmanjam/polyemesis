// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";

import { saveStateOf, useSaveModel } from "./saveModel";

afterEach(cleanup);

/* THE SAVE STATE IS DERIVED FROM FOUR FACTS, AND THE ORDER IS THE POINT.
 * A pending save wins, then a failure (so a retry is never hidden behind a
 * stale "Saved"), then the draft against the stored value, and "saved" only
 * while nothing has been edited since the write. */
describe("saveStateOf", () => {
  it("puts a pending save above everything, and a failure above a dirty draft", () => {
    expect(saveStateOf({ dirty: true, saving: true, failed: true, justSaved: false })).toBe("saving");
    expect(saveStateOf({ dirty: true, saving: false, failed: true, justSaved: false })).toBe("failed");
    expect(saveStateOf({ dirty: true, saving: false, failed: false, justSaved: true })).toBe("dirty");
  });

  it("says saved only when nothing differs, and clean otherwise", () => {
    expect(saveStateOf({ dirty: false, saving: false, failed: false, justSaved: true })).toBe("saved");
    expect(saveStateOf({ dirty: false, saving: false, failed: false, justSaved: false })).toBe("clean");
  });
});

describe("useSaveModel", () => {
  it("starts clean, becomes dirty on an edit, and returns to saved after a successful write", async () => {
    const onSave = vi.fn(async () => {});
    const { result } = renderHook(() => useSaveModel({ stored: 5, onSave }));
    expect(result.current.state).toBe("clean");

    act(() => result.current.setDraft(7));
    expect(result.current.dirty).toBe(true);
    expect(result.current.state).toBe("dirty");

    await act(async () => {
      await result.current.save();
    });
    expect(onSave).toHaveBeenCalledWith(7);
    expect(result.current.state).toBe("saved");
  });

  it("keeps the draft on a failed write so the operator can retry without retyping", async () => {
    const onSave = vi.fn(async () => {
      throw new Error("network");
    });
    const { result } = renderHook(() => useSaveModel({ stored: "a", onSave }));
    act(() => result.current.setDraft("b"));
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.state).toBe("failed");
    expect(result.current.draft).toBe("b");
  });

  it("discard returns the draft to the stored value", () => {
    const { result } = renderHook(() => useSaveModel({ stored: 1, onSave: async () => {} }));
    act(() => result.current.setDraft(9));
    act(() => result.current.discard());
    expect(result.current.draft).toBe(1);
    expect(result.current.state).toBe("clean");
  });

  it("reseeds a clean draft when the stored value moves, but never overwrites a dirty one", () => {
    const { result, rerender } = renderHook(
      ({ stored }: { stored: number }) => useSaveModel({ stored, onSave: async () => {} }),
      { initialProps: { stored: 1 } },
    );
    rerender({ stored: 2 });
    expect(result.current.draft).toBe(2);

    act(() => result.current.setDraft(50));
    rerender({ stored: 3 });
    expect(result.current.draft).toBe(50);
    expect(result.current.dirty).toBe(true);
  });
});
