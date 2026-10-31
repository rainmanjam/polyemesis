// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { ChatStatus } from "@/lib/types";

/* A42 and A52, on the Chat sidebar.
 *
 * A42: the attention banner said "1 platform needs attention" and nothing
 * else, so the moderator had to scan every card to find which platform was
 * failing and why. The banner now names each troubled platform with the
 * reason the server gave.
 *
 * A52: the "no account attached" line told a new install to go to Settings →
 * Platform credentials and gave no way there. It now links to that tab, which
 * is the same tab the Settings deep link points at elsewhere.
 *
 * The feed hook is mocked because the real one opens a WebSocket. */

let statuses: ChatStatus[] = [];

vi.mock("@/hooks/useChatFeed", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useChatFeed")>()),
  useChatFeed: () => ({
    messages: [],
    statuses,
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

beforeEach(() => {
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = function () {};
  }
});
afterEach(cleanup);

describe("the attention banner (A42)", () => {
  it("names the failing platform and gives the reason, not only a count", () => {
    statuses = [
      {
        platform: "twitch",
        account: "acct",
        state: "failed",
        detail: "The token was rejected; sign in again",
        since: "2026-08-20T12:00:00Z",
        received: 0,
        sent: 0,
        restarts: 0,
        canSend: true,
      },
      {
        platform: "youtube",
        account: "acct",
        state: "live",
        since: "2026-08-20T12:00:00Z",
        received: 4,
        sent: 0,
        restarts: 0,
        canSend: true,
      },
    ];
    render(
      <MemoryRouter>
        <ChatPage />
      </MemoryRouter>,
    );
    expect(
      screen.getByText(/Twitch: The token was rejected; sign in again/),
      "the banner says that one platform needs attention but not which one or why",
    ).toBeTruthy();
  });
});

describe("the no-account line (A52)", () => {
  it("links to the platform credentials tab, not only describes where it is", () => {
    statuses = [];
    render(
      <MemoryRouter>
        <ChatPage />
      </MemoryRouter>,
    );
    const link = screen.getByRole("link", { name: "Open platform credentials" });
    expect(link.getAttribute("href")).toBe("/settings?tab=platforms");
  });
});
