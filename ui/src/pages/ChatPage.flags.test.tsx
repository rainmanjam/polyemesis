// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { api } from "@/lib/api";
import type { ChatFlaggedMessage, ChatFlagsResult, ChatMessage, ChatPlatform } from "@/lib/types";
import { FLAGS_REFRESH_MS } from "@/features/chat/useChatFlags";

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

/** A server answer: counts are over the whole list, as the server sends them. */
const result = (flags: ChatFlaggedMessage[], truncated = false): ChatFlagsResult => {
  const counts: Partial<Record<ChatPlatform, number>> = {};
  for (const f of flags) counts[f.message.platform] = (counts[f.message.platform] ?? 0) + 1;
  return { flags, truncated, counts, note: "server prose the UI must not render" };
};

const chatFlags = vi.spyOn(api, "chatFlags");
const chatSearch = vi.spyOn(api, "chatSearch");
const chatUser = vi.spyOn(api, "chatUser");

beforeEach(() => {
  chatFlags.mockReset();
  chatUser.mockReset();
  chatSearch.mockReset();
  chatSearch.mockResolvedValue({ query: "", messages: [], truncated: false, retentionNote: "" } as never);
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
    chatFlags.mockResolvedValue(result([flag("twitch", "t1", "ana", true), flag("youtube", "y1", "bo")]));
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
    // review #8: the coverage line comes from the catalogue, not the server's
    // English, and a category's English reason is not repeated after it.
    expect(screen.getByText(/empty again after a restart/)).toBeTruthy();
    expect(screen.queryByText(/server prose/)).toBeNull();
    expect(screen.queryByText(/matched rule/)).toBeNull();
  });

  it("is filtered by the same platform toggles as the timeline", async () => {
    chatFlags.mockResolvedValue(result([flag("twitch", "t1", "ana"), flag("youtube", "y1", "bo")]));
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
    chatFlags.mockResolvedValue(result([]));
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

  it("offers Show on overlay only for a held message, and releases it", async () => {
    const held = flag("twitch", "t1", "ana");
    held.findings = [{ checker: "rules", action: "flag", reason: 'matched rule "links"', hold: true }];
    held.held = true;
    chatFlags.mockResolvedValue(result([held, flag("youtube", "y1", "bo")]));
    const release = vi.spyOn(api, "releaseChatFlag").mockResolvedValue({ status: "released", detail: "" });
    draw();
    await waitFor(() => expect(chip().textContent).toContain("2"));
    fireEvent.click(chip());
    const list = await screen.findByRole("list", { name: "Messages automod flagged" });
    expect(within(list).getAllByText("Held off overlays")).toHaveLength(1);
    expect(within(list).getAllByRole("button", { name: /Show .* message on the overlay/ })).toHaveLength(1);

    const before = chatFlags.mock.calls.length;
    // The server stops marking the line held once it is released.
    const after = flag("twitch", "t1", "ana");
    chatFlags.mockResolvedValue(result([after, flag("youtube", "y1", "bo")]));
    fireEvent.click(within(list).getByRole("button", { name: "Show ana's message on the overlay" }));
    await waitFor(() => expect(release).toHaveBeenCalledWith({ platform: "twitch", account: "acct", id: "t1" }));
    // The list is read again, and the button goes with the hold.
    await waitFor(() => expect(chatFlags.mock.calls.length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.queryByRole("button", { name: /on the overlay/ })).toBeNull());
    release.mockRestore();
  });

  // A finding that says hold but whose line was removed (or whose switch was
  // off) went out or was deleted: nothing to release, so no button.
  it("offers no Show on overlay for a finding that was not actually held", async () => {
    const f = flag("twitch", "t1", "ana");
    f.findings = [{ checker: "rules", action: "delete", reason: 'matched rule "links"', hold: true }];
    chatFlags.mockResolvedValue(result([f]));
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1"));
    fireEvent.click(chip());
    await screen.findByRole("list", { name: "Messages automod flagged" });
    expect(screen.queryByText("Held off overlays")).toBeNull();
    expect(screen.queryByRole("button", { name: /on the overlay/ })).toBeNull();
  });

  it("opens the flagged author's user card", async () => {
    chatFlags.mockResolvedValue(result([flag("twitch", "t1", "ana")]));
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1"));
    fireEvent.click(chip());
    fireEvent.click(await screen.findByRole("button", { name: "Open the user card for ana" }));
    await waitFor(() => expect(chatUser).toHaveBeenCalled());
    expect(chatUser.mock.calls[0][0]).toMatchObject({ platform: "twitch", account: "acct", authorId: "u-t1" });
  });

  // review #9: the count is exact per platform, so a filtered view never reads
  // "0+" off the unfiltered list's truncation.
  it("never adds a + from the unfiltered list to a filtered count", async () => {
    chatFlags.mockResolvedValue(result([flag("youtube", "y1", "bo")], true));
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1"));
    fireEvent.click(screen.getByRole("button", { name: /YouTube/, pressed: true }));
    await waitFor(() => expect(chip().textContent).toMatch(/Flagged\s*0$/));
    expect(chip().textContent).not.toContain("+");
  });

  // review #8: a rule finding has no category, so its reason -- the
  // operator's rule name -- is the only "why" there is.
  it("shows a finding's reason only when it has no category", async () => {
    const f = flag("twitch", "t1", "ana");
    f.findings = [{ checker: "rules", action: "flag", reason: 'matched rule "links"' }];
    chatFlags.mockResolvedValue(result([f]));
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1"));
    fireEvent.click(chip());
    expect(await screen.findByText(/matched rule "links"/)).toBeTruthy();
  });

  // review #3: the full list only while it is open.
  it("asks for one entry while closed and the whole list while open", async () => {
    chatFlags.mockResolvedValue(result([flag("twitch", "t1", "ana")]));
    draw();
    await waitFor(() => expect(chatFlags).toHaveBeenCalled());
    expect(chatFlags.mock.calls[0][0]).toEqual({ limit: 1 });
    fireEvent.click(chip());
    await waitFor(() => expect(chatFlags.mock.calls.at(-1)?.[0]).toEqual({ limit: 500 }));
  });

  // review #10: search wins over the list, rather than firing invisibly.
  it("shows search results, not the list, once a search is typed", async () => {
    chatFlags.mockResolvedValue(result([flag("twitch", "t1", "ana")]));
    draw();
    await waitFor(() => expect(chip().textContent).toContain("1"));
    fireEvent.click(chip());
    await screen.findByRole("list", { name: "Messages automod flagged" });
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "hello" } });
    await waitFor(() => expect(screen.queryByRole("list", { name: "Messages automod flagged" })).toBeNull());
    expect(chip().getAttribute("aria-pressed")).toBe("false");
  });
});

describe("the Flagged poll (review #3)", () => {
  it("does not poll while the tab is hidden", async () => {
    vi.useFakeTimers();
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    try {
      chatFlags.mockResolvedValue(result([]));
      draw();
      const first = chatFlags.mock.calls.length;
      expect(first).toBe(1);
      await vi.advanceTimersByTimeAsync(FLAGS_REFRESH_MS * 3);
      expect(chatFlags.mock.calls.length).toBe(first);
      hidden.mockReturnValue(false);
      await vi.advanceTimersByTimeAsync(FLAGS_REFRESH_MS);
      expect(chatFlags.mock.calls.length).toBe(first + 1);
    } finally {
      hidden.mockRestore();
      vi.useRealTimers();
    }
  });
});
