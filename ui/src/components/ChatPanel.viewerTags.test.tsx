// @vitest-environment jsdom

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { ChatTimeline } from "./ChatPanel";
import { resetViewerTags } from "@/hooks/useViewerTags";
import { api } from "@/lib/api";
import type { ChatMessage } from "@/lib/types";

/* A viewer the operator has tagged shows a small badge beside their name in the
 * chat list. One lookup serves the whole list; a viewer in another channel with
 * the same author id is a different viewer. */

beforeAll(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
});
const viewerTags = vi.spyOn(api, "viewerTags");
beforeEach(() => {
  resetViewerTags();
  viewerTags.mockReset();
});
afterEach(cleanup);

const msg = (id: string, account: string, authorId: string, name: string) =>
  ({ id, platform: "twitch", account, author: { id: authorId, name }, text: "hi", at: "" }) as ChatMessage;

describe("tag badge in the chat list", () => {
  it("marks the tagged viewer in that account only, as text", async () => {
    viewerTags.mockResolvedValue({
      viewers: [{ platform: "twitch", account: "a", authorId: "u1", tags: ["<i>troll</i>", "watch"] }],
      presetTags: [],
    });
    render(<ChatTimeline messages={[msg("1", "a", "u1", "tagged"), msg("2", "b", "u1", "other"), msg("3", "a", "u2", "plain")]} />);
    const badge = await screen.findByTestId("viewer-tag-badge");
    expect(badge.textContent).toBe("<i>troll</i> +1");
    expect(badge.title).toBe("Tags: <i>troll</i>, watch");
    expect(screen.getAllByTestId("viewer-tag-badge")).toHaveLength(1);
    expect(document.body.querySelector("i")).toBeNull();
  });

  it("shows no badge, and no error, when the lookup fails", async () => {
    viewerTags.mockRejectedValue(new Error("403"));
    render(<ChatTimeline messages={[msg("1", "a", "u1", "tagged")]} />);
    await waitFor(() => expect(viewerTags).toHaveBeenCalled());
    expect(screen.queryByTestId("viewer-tag-badge")).toBeNull();
  });
});
