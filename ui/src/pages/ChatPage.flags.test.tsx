// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { api } from "@/lib/api";
import type { ChatFlaggedMessage, ChatMessage } from "@/lib/types";

/* UX COUNCIL A48: AUTOMOD'S FLAGS, READABLE ON CHAT.
 *
 * Automod's findings went to the server log and nowhere else. The Chat page now
 * carries a Flagged chip with a count, backed by GET /chat/flags, and the list
 * it opens honours the same platform filter as the timeline.
 *
 * The feed hook is mocked because the real one opens a WebSocket. */

const messages: ChatMessage[] = [
  { id: "t0", platform: "twitch", author: { id: "x", name: "x" }, text: "hi", at: "2026-11-01T18:00:00Z" },
  { id: "y0", platform: "youtube", author: { id: "y", name: "y" }, text: "hi", at: "2026-11-01T18:00:00Z" },
] as ChatMessage[];

vi.mock("@/hooks/useChatFeed", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useChatFeed")>()),
  useChatFeed: () => ({
    messages,
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
    remove: vi.fn(),
  }),
}));

const { ChatPage } = await import("./ChatPage");

const flag = (platform: "twitch" | "youtube", id: string, name: string, acted = false): ChatFlaggedMessage => ({
  message: { id, platform, account: "acct", author: { id: `u-${id}`, name }, text: `spam from ${name}`, at: "2026-11-01T18:00:01Z" } as ChatMessage,
  findings: [{ checker: "rules", action: "flag", category: "spam", reason: 'matched rule "links"' }],
  acted,
  at: "2026-11-01T18:00:02Z",
});

const chatFlags = vi.spyOn(api, "chatFlags");
const chatUser = vi.spyOn(api, "chatUser");

beforeEach(() => {
  chatFlags.mockReset();
  chatUser.mockReset();
  chatUser.mockResolvedValue({ platform: "twitch", authorId: "u-t1", messages: [], truncated: false, retentionNote: "" });
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
});
afterEach(cleanup);

const draw = () =>
  render(
    <MemoryRouter>
      <ChatPage />
    </MemoryRouter>,
  );

const chip = () => screen.getByRole("button", { name: /Show messages automod flagged/ });

describe("the Flagged chip (A48)", () => {
  it("counts the flagged messages and opens the list", async () => {
    chatFlags.mockResolvedValue({
      flags: [flag("twitch", "t1", "ana", true), flag("youtube", "y1", "bo")],
      truncated: false,
      note: "Messages automod flagged since this server started.",
    });
    draw();
    await waitFor(() => expect(chip().textContent).toContain("2"));
    expect(chip().getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(chip());
    expect(chip().getAttribute("aria-pressed")).toBe("true");
    const list = await screen.findByRole("list", { name: "Messages automod flagged" });
    expect(within(list).getByText("spam from ana")).toBeTruthy();
    expect(within(list).getByText("spam from bo")).toBeTruthy();
    // The finding in the operator's words, and whether automod acted.
    expect(within(list).getAllByText(/Spam · Rules · Flag for review/).length).toBe(2);
    expect(within(list).getByText("Acted on")).toBeTruthy();
    expect(within(list).getByText("Logged only")).toBeTruthy();
    expect(screen.getByText(/since this server started/)).toBeTruthy();
  });

  it("is filtered by the same platform toggles as the timeline", async () => {
    chatFlags.mockResolvedValue({
      flags: [flag("twitch", "t1", "ana"), flag("youtube", "y1", "bo")],
      truncated: false,
      note: "",
    });
    draw();
    await waitFor(() => expect(chip().textContent).toContain("2"));
    fireEvent.click(screen.getByRole("button", { name: /YouTube/, pressed: true }));
    await waitFor(() => expect(chip().textContent).toContain("1"));

    fireEvent.click(chip());
    const list = await screen.findByRole("list", { name: "Messages automod flagged" });
    expect(within(list).getByText("spam from ana")).toBeTruthy();
    expect(within(list).queryByText("spam from bo")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Twitch/, pressed: true }));
    expect(await screen.findByText("Nothing flagged")).toBeTruthy();
    expect(screen.getByText(/Turn a platform back on/)).toBeTruthy();
  });

  it("says nothing was flagged only when the server said so", async () => {
    chatFlags.mockResolvedValue({ flags: [], truncated: false, note: "" });
    draw();
    await waitFor(() => expect(chip().textContent).toContain("0"));
    fireEvent.click(chip());
    expect(await screen.findByText("Nothing flagged")).toBeTruthy();
    expect(screen.getByText(/has not flagged any message/)).toBeTruthy();
  });

  it("does not report a failed read as nothing flagged", async () => {
    chatFlags.mockRejectedValue(new Error("down"));
    draw();
    await waitFor(() => expect(chatFlags).toHaveBeenCalled());
    fireEvent.click(chip());
    expect(await screen.findByText("Flags not loaded")).toBeTruthy();
    expect(screen.queryByText("Nothing flagged")).toBeNull();
  });

  it("opens the flagged author's user card", async () => {
    chatFlags.mockResolvedValue({ flags: [flag("twitch", "t1", "ana")], truncated: true, note: "" });
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1+"));
    fireEvent.click(chip());
    fireEvent.click(await screen.findByRole("button", { name: "Open the user card for ana" }));
    await waitFor(() => expect(chatUser).toHaveBeenCalled());
    expect(chatUser.mock.calls[0][0]).toMatchObject({ platform: "twitch", account: "acct", authorId: "u-t1" });
  });
});
