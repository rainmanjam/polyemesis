// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";

import { usePreviewTiles } from "./usePreviewTiles";

const tile = (id: number, outputLive = true) => ({
  id,
  name: `s${id}`,
  outputLive,
  ingestLive: outputLive,
});

/* The risky half of this hook is not the fetching, it is the ORDERING.
 *
 * setInterval will start another request while a slow one is still in the air,
 * and the responses can land out of order. An older outputLive:true overwriting
 * a newer false lifts the opaque overlay and puts the stale frame back on
 * screen -- which is the exact bug the grid exists to fix, reintroduced by the
 * thing that feeds it.
 */
describe("usePreviewTiles", () => {
  beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  const respond = (body: unknown) =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve(body),
    } as Response);

  it("reads tiles when enabled", async () => {
    vi.mocked(fetch).mockImplementation(() => respond([tile(1)]));
    const { result } = renderHook(() => usePreviewTiles(true));
    await waitFor(() => expect(result.current).toHaveLength(1));
    expect(result.current[0].name).toBe("s1");
  });

  it("asks for nothing at all when disabled", () => {
    renderHook(() => usePreviewTiles(false));
    expect(fetch).not.toHaveBeenCalled();
  });

  it("issues NO second request while one is still in the air", async () => {
    // Single-flight is what actually prevents the out-of-order case: a stale
    // response cannot overwrite a newer one if a newer one was never started.
    // The sequence check in the hook is defence behind this, not instead of it.
    let release: (v: unknown) => void = () => {};
    const hanging = new Promise((r) => (release = r));
    vi.mocked(fetch).mockImplementation(
      () =>
        hanging.then(() => ({
          ok: true,
          json: () => Promise.resolve([tile(1)]),
        })) as never,
    );

    renderHook(() => usePreviewTiles(true, 10));
    // Several intervals elapse while the first request hangs.
    await new Promise((r) => setTimeout(r, 80));
    expect(
      vi.mocked(fetch).mock.calls.length,
      "a poll was issued while another was still in the air, so their responses " +
        "can land out of order and an older outputLive:true can lift the offline " +
        "overlay back off a stale frame",
    ).toBe(1);

    await act(async () => {
      release(null);
      await Promise.resolve();
    });
  });

  it("keeps the last answer when a poll fails, rather than blanking the grid", async () => {
    vi.mocked(fetch).mockImplementationOnce(() => respond([tile(1)]));
    const { result } = renderHook(() => usePreviewTiles(true, 20));
    await waitFor(() => expect(result.current).toHaveLength(1));

    vi.mocked(fetch).mockImplementation(() =>
      Promise.reject(new Error("offline")),
    );
    await new Promise((r) => setTimeout(r, 60));
    expect(
      result.current,
      "one dropped request flickered every tile",
    ).toHaveLength(1);
  });
});
