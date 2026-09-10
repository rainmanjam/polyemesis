// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import type { ChatMessage } from "@/lib/types";

/* #770: deleting a chat message fired on the first click.
 *
 * The trash icon on a row, the Delete item in the right-click menu and the
 * same item over a search result all reached the platform immediately — while
 * a permanent ban, two items above Delete in that same menu, deliberately
 * refuses to be one click and routes through the user card to be confirmed.
 * Two irreversible actions a centimetre apart, one guarded and one not, is
 * the inconsistency ConfirmDestructive's own header names as the reason it
 * exists: an operator who learns "deletes ask first" and meets one that does
 * not has had their caution trained out of them exactly where it mattered.
 *
 * And this is the delete with the smallest target in the product: an icon that
 * appears on hover, at the edge of a row, in a list that scrolls on its own
 * while a stream is live.
 *
 * The feed hook is mocked because the real one opens a WebSocket. jsdom
 * because ConfirmDestructive is a Radix dialog rendered through a portal.
 */

const remove = vi.fn();

vi.mock("@/hooks/useChatFeed", async (importOriginal) => ({
  // Partial: ChatPanel imports messageKey from this module too, and replacing
  // the whole thing takes that with it.
  ...(await importOriginal<typeof import("@/hooks/useChatFeed")>()),
  useChatFeed: () => ({
    messages: [
      {
        id: "m1",
        platform: "twitch",
        account: "acct",
        text: "the offending line",
        at: "2026-08-20T12:00:00Z",
        author: { id: "u1", name: "someone" },
      } as ChatMessage,
    ],
    statuses: [],
    limits: [],
    stats: null,
    configured: true,
    connected: true,
    loading: false,
    stored: false,
    error: "",
    frameError: false,
    reload: vi.fn(),
    remove,
  }),
}));

import { api } from "@/lib/api";
import { ChatPage } from "./ChatPage";

const trash = () => screen.getByRole("button", { name: /Delete message from someone/ });
const confirm = () => screen.getByRole("button", { name: "Delete for everyone" });
const chatSearch = vi.spyOn(api, "chatSearch");

/** The message row, which opens the quick-action menu on right-click. */
const row = () => screen.getByText("the offending line").closest("div") as HTMLElement;

beforeEach(() => {
  // jsdom implements no scrolling at all, and the timeline pins itself to the
  // bottom on every new message. Same stub ChatPanel.filter.test.tsx uses.
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
  remove.mockReset();
  remove.mockResolvedValue(undefined);
  chatSearch.mockReset();
});
afterEach(cleanup);

describe("deleting a chat message", () => {
  it("does not reach the platform on the first click", async () => {
    render(<ChatPage />);
    fireEvent.click(trash());
    await waitFor(() => expect(screen.getByText("Delete this message?")).toBeTruthy());
    expect(remove).not.toHaveBeenCalled();
  });

  it("quotes the message, so the operator can see which one this is", async () => {
    // The actual guard against the mis-click this exists for. The pointer is
    // over one message when the decision is made and over the next one by the
    // time the hover-revealed icon arrives under it.
    render(<ChatPage />);
    fireEvent.click(trash());
    // Scoped to the dialog: the line is also still on the timeline behind it,
    // which is the point — the operator compares the two.
    await waitFor(() => expect(screen.getByRole("dialog")).toBeTruthy());
    expect(within(screen.getByRole("dialog")).getByText("the offending line")).toBeTruthy();
  });

  it("names where the message goes, and what the reversible option is", async () => {
    render(<ChatPage />);
    fireEvent.click(trash());
    await waitFor(() => expect(screen.getByText(/removed on Twitch/)).toBeTruthy());
    expect(screen.getByText(/hide it instead/)).toBeTruthy();
  });

  it("deletes once confirmed", async () => {
    render(<ChatPage />);
    fireEvent.click(trash());
    await waitFor(() => expect(confirm()).toBeTruthy());
    fireEvent.click(confirm());
    await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect((remove.mock.calls[0][0] as ChatMessage).id).toBe("m1");
  });

  it("deletes nothing when the operator backs out", async () => {
    render(<ChatPage />);
    fireEvent.click(trash());
    await waitFor(() => expect(confirm()).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Delete this message?")).toBeNull());
    expect(remove).not.toHaveBeenCalled();
  });

  it("asks again for the next message rather than staying unlocked", async () => {
    // A confirmation that a previous one left dismissed is not a confirmation.
    render(<ChatPage />);
    fireEvent.click(trash());
    await waitFor(() => expect(confirm()).toBeTruthy());
    fireEvent.click(confirm());
    await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByText("Delete this message?")).toBeNull());

    fireEvent.click(trash());
    await waitFor(() => expect(screen.getByText("Delete this message?")).toBeTruthy());
    expect(remove).toHaveBeenCalledTimes(1);
  });
});

/* ALL THREE ENTRY POINTS, because a per-caller confirmation is how one of them
 * ends up without one — which is the whole reason the dialog lives on the page
 * rather than in each of the three components that can start a delete. */
describe("every way into a delete", () => {
  it("asks when the delete comes from the right-click menu", async () => {
    render(<ChatPage />);
    fireEvent.contextMenu(row());
    await waitFor(() =>
      expect(screen.getByRole("menuitem", { name: /Delete message/ })).toBeTruthy(),
    );
    fireEvent.click(screen.getByRole("menuitem", { name: /Delete message/ }));
    await waitFor(() => expect(screen.getByText("Delete this message?")).toBeTruthy());
    expect(remove).not.toHaveBeenCalled();
  });

  it("asks when the delete comes from a search result", async () => {
    // The result list is a third component with its own row and its own trash
    // icon, wired to the same handler — and nothing but this test says so.
    chatSearch.mockResolvedValue({
      query: "offending",
      messages: [
        {
          id: "m1",
          platform: "twitch",
          account: "acct",
          text: "the offending line",
          at: "2026-08-20T12:00:00Z",
          author: { id: "u1", name: "someone" },
        } as ChatMessage,
      ],
      truncated: false,
      retentionNote: "",
    });
    render(<ChatPage />);
    fireEvent.change(screen.getByLabelText("Search chat history"), {
      target: { value: "offending" },
    });
    // One result row, and the timeline is replaced by it rather than shown
    // beside it, so there is exactly one trash icon on screen.
    await waitFor(() => expect(chatSearch).toHaveBeenCalled());
    await waitFor(() => expect(trash()).toBeTruthy());
    fireEvent.click(trash());
    await waitFor(() => expect(screen.getByText("Delete this message?")).toBeTruthy());
    expect(remove).not.toHaveBeenCalled();
  });
});
