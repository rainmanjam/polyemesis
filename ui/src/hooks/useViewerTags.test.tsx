// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";

import { refreshViewerTags, resetViewerTags, useViewerTags } from "./useViewerTags";
import { api } from "@/lib/api";

/* A refresh asked for while a read is in flight must not be dropped: a note
 * saved a moment after the read began would leave the badge stale. */

const viewerTags = vi.spyOn(api, "viewerTags");
beforeEach(() => {
  resetViewerTags();
  viewerTags.mockReset();
});
afterEach(cleanup);

function Probe() {
  const tags = useViewerTags("twitch", "a", "u1");
  return <span data-testid="t">{tags.join(",")}</span>;
}

describe("useViewerTags", () => {
  it("re-reads once after the in-flight read when a refresh was asked for meanwhile", async () => {
    let release: (v: unknown) => void = () => {};
    viewerTags.mockImplementationOnce(() => new Promise((res) => (release = res)) as never);
    viewerTags.mockResolvedValue({ viewers: [{ platform: "twitch", account: "a", authorId: "u1", tags: ["new"] }], presetTags: [] });
    const { getByTestId } = render(<Probe />);
    await waitFor(() => expect(viewerTags).toHaveBeenCalledTimes(1));
    refreshViewerTags();
    refreshViewerTags(); // two asks collapse into one re-run
    expect(viewerTags).toHaveBeenCalledTimes(1);
    release({ viewers: [], presetTags: [] });
    await waitFor(() => expect(getByTestId("t").textContent).toBe("new"));
    expect(viewerTags).toHaveBeenCalledTimes(2);
  });
});
