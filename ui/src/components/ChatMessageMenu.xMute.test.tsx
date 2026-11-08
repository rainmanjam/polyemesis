// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatMessageMenu } from "./ChatMessageMenu";
import { api } from "@/lib/api";
import type { ChatMessage } from "@/lib/types";

const ban = vi.spyOn(api, "banChatUser");

const msg = (platform: string): ChatMessage =>
  ({
    id: "1761904800123456789",
    platform,
    account: "acct",
    text: "hello",
    at: "2026-11-07T00:00:00Z",
    author: { id: "555", name: "someone" },
  }) as ChatMessage;

const draw = (m: ChatMessage) =>
  render(<ChatMessageMenu message={m} anchor={{ x: 1, y: 1 }} onClose={() => {}} onOpenCard={() => {}} />);

describe("mute and remove on X", () => {
  afterEach(() => {
    cleanup();
    ban.mockReset();
  });

  it("is offered on X and sends the message id with the ban in one request", async () => {
    ban.mockResolvedValue({ status: "banned" });
    draw(msg("x"));
    fireEvent.click(screen.getByRole("menuitem", { name: /Mute and remove this message/ }));
    await waitFor(() =>
      expect(ban).toHaveBeenCalledWith({
        platform: "x",
        account: "acct",
        userId: "555",
        messageId: "1761904800123456789",
      }),
    );
  });

  it("is not offered on a platform that has no such call", () => {
    draw(msg("twitch"));
    expect(screen.queryByRole("menuitem", { name: /Mute and remove this message/ })).toBeNull();
  });
});

describe("mute and remove needs an id X can address", () => {
  afterEach(cleanup);
  it("is inert for a message with a synthesised id", () => {
    draw({ ...msg("x"), id: "syn-x-123" } as ChatMessage);
    expect(screen.getByRole("menuitem", { name: /Mute and remove this message/ }).getAttribute("aria-disabled")).toBe("true");
  });
});
