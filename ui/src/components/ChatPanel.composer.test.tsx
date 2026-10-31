// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import type { ChatMessage, ChatPlatform, ChatStatus } from "@/lib/types";

/* ChatPanel had ten of its hundred and fifty-six statements covered, and the
 * one it documents most carefully -- what an empty timeline SAYS -- was among
 * the uncovered ones.
 *
 * That matters more here than a percentage does. ChatEmpty exists because "no
 * messages" is true in five different situations and useful in none of them,
 * and the fifth was a real bug: the component was handed the FILTERED list and
 * knew nothing about the filter, so with every talking platform switched off it
 * said "Connected and waiting. Nothing has been said yet." over a scrollback
 * that was full. A moderator reading that sentence stops looking.
 *
 * The hooks are mocked because the pane's whole job is to render what
 * internal/chat states, and this file is about that rendering. What is NOT
 * mocked is the filter, the empty-state ranking or the delete path -- those are
 * the component. */

const remove = vi.fn();

interface FeedState {
  messages: ChatMessage[];
  statuses: ChatStatus[];
  limits: unknown[];
  configured: boolean;
  loading: boolean;
  connected: boolean;
  stored: boolean;
  error: string;
  frameError: boolean;
}

let feed: FeedState;

vi.mock("@/hooks/useChatFeed", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useChatFeed")>();
  return {
    ...actual,
    useChatFeed: () => ({ ...feed, reload: vi.fn(), remove }),
  };
});

vi.mock("@/hooks/useChatSearch", () => ({
  useChatSearch: () => ({
    query: "",
    setQuery: vi.fn(),
    active: false,
    results: [],
    loading: false,
    error: "",
    truncated: false,
    note: "",
    clear: vi.fn(),
  }),
}));

const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn(), message: vi.fn() },
}));

// Imported after the mocks so the component picks them up.
const { ChatPanel } = await import("./ChatPanel");


function status(platform: ChatPlatform): ChatStatus {
  return {
    platform,
    account: `${platform}-acct`,
    state: "live",
    since: "2026-01-01T09:00:00Z",
    received: 1,
    sent: 0,
    restarts: 0,
    canSend: true,
  };
}

beforeEach(() => {
  // jsdom implements no scrolling at all, and the timeline pins itself to the
  // bottom on every new message. Not a product concern -- the assertion is
  // never about the scroll -- but without this every render throws.
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
  remove.mockReset();
  toastError.mockReset();
  feed = {
    messages: [],
    statuses: [],
    limits: [],
    configured: true,
    loading: false,
    connected: true,
    stored: false,
    error: "",
    frameError: false,
  };
});

afterEach(cleanup);

describe("the composer's placeholder when nothing can send (A88)", () => {
  it("keeps the placeholder short and puts the reason beneath the input, not clipped inside it", () => {
    // The placeholder used to carry the whole sentence, and the card clipped it
    // mid-sentence. The short state now sits in the field and the reason is
    // readable text under it.
    feed.statuses = [{ ...status("twitch"), canSend: false }];
    render(<ChatPanel />);
    const input = screen.getByRole("textbox", { name: /message/i }) as HTMLTextAreaElement;
    expect(input.placeholder).toBe("Chat not connected");
    expect(screen.getByText("No connected platform can send right now — you can still type.")).toBeTruthy();
  });
});
