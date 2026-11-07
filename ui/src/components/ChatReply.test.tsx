// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { ChatMessage, ChatStatus } from "@/lib/types";

/* Replying to one message is Kick-only. The menu offers it live on Kick and
 * inert, with the reason, elsewhere; the composer then sends to that one
 * platform account instead of fanning out, and says so. */

const sendChat = vi.fn();
const replyChat = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    sendChat: (...a: unknown[]) => sendChat(...a),
    replyChat: (...a: unknown[]) => replyChat(...a),
  },
}));
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() },
}));

const { ChatComposer } = await import("./ChatPanel");
const { ChatMessageMenu } = await import("./ChatMessageMenu");

const msg = (platform: ChatMessage["platform"]): ChatMessage =>
  ({
    id: "m1",
    platform,
    account: "acct",
    text: "hello there",
    at: "2026-01-01T09:00:00Z",
    author: { id: "u1", name: "viewer" },
  }) as ChatMessage;

const statuses: ChatStatus[] = [
  {
    platform: "kick",
    account: "acct",
    state: "live",
    since: "2026-01-01T09:00:00Z",
    received: 0,
    sent: 0,
    restarts: 0,
    canSend: true,
  },
];

beforeEach(() => {
  sendChat.mockReset();
  replyChat.mockReset();
  replyChat.mockResolvedValue({ results: [], sent: 1, failed: 0, skipped: 0 });
});
afterEach(cleanup);

describe("the Reply menu item", () => {
  const draw = (m: ChatMessage, onReply = vi.fn()) => {
    render(
      <ChatMessageMenu
        message={m}
        anchor={{ x: 1, y: 1 }}
        onClose={() => {}}
        onOpenCard={() => {}}
        onReply={onReply}
      />,
    );
    return onReply;
  };

  it("is live on Kick and hands the message over", () => {
    const onReply = draw(msg("kick"));
    const item = screen.getByRole("menuitem", { name: /reply/i });
    expect(item.getAttribute("aria-disabled")).not.toBe("true");
    fireEvent.click(item);
    expect(onReply).toHaveBeenCalledWith(expect.objectContaining({ id: "m1" }));
  });

  it("is inert on a platform without threaded replies", () => {
    draw(msg("twitch"));
    const item = screen.getByRole("menuitem", { name: /reply/i });
    expect(item.getAttribute("aria-disabled")).toBe("true");
  });
});

describe("the composer while replying", () => {
  it("sends to the one message, as the user by default", async () => {
    const onCancel = vi.fn();
    render(
      <ChatComposer
        statuses={statuses}
        limits={[]}
        configured
        replyTo={msg("kick")}
        onCancelReply={onCancel}
      />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "hi" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(replyChat).toHaveBeenCalled());
    expect(replyChat).toHaveBeenCalledWith(
      { platform: "kick", account: "acct", id: "m1" },
      "hi",
      false,
    );
    expect(sendChat).not.toHaveBeenCalled();
    await waitFor(() => expect(onCancel).toHaveBeenCalled());
  });

  it("sends as the bot when asked", async () => {
    render(
      <ChatComposer statuses={statuses} limits={[]} configured replyTo={msg("kick")} />,
    );
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "beep" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(replyChat).toHaveBeenCalled());
    expect(replyChat.mock.calls[0][2]).toBe(true);
  });

  it("keeps the reply open when no platform took it", async () => {
    replyChat.mockResolvedValue({
      results: [{ platform: "kick", ok: false, detail: "no" }],
      sent: 0,
      failed: 1,
      skipped: 0,
    });
    const onCancel = vi.fn();
    render(
      <ChatComposer
        statuses={statuses}
        limits={[]}
        configured
        replyTo={msg("kick")}
        onCancelReply={onCancel}
      />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "hi" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(replyChat).toHaveBeenCalled());
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("cancels", () => {
    const onCancel = vi.fn();
    render(
      <ChatComposer
        statuses={statuses}
        limits={[]}
        configured
        replyTo={msg("kick")}
        onCancelReply={onCancel}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /cancel reply/i }));
    expect(onCancel).toHaveBeenCalled();
  });
});

describe("reply state", () => {
  it("does not carry Send as bot into the next reply", async () => {
    const { rerender } = render(
      <ChatComposer statuses={statuses} limits={[]} configured replyTo={msg("kick")} />,
    );
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "one" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(replyChat).toHaveBeenCalledTimes(1));
    expect(replyChat.mock.calls[0][2]).toBe(true);

    rerender(
      <ChatComposer
        statuses={statuses}
        limits={[]}
        configured
        replyTo={{ ...msg("kick"), id: "m2" }}
      />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "two" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(replyChat).toHaveBeenCalledTimes(2));
    expect(replyChat.mock.calls[1][2]).toBe(false);
  });

  it("hands back the id it sent so a late finish cannot drop a newer target", async () => {
    const onCancel = vi.fn();
    render(
      <ChatComposer
        statuses={statuses}
        limits={[]}
        configured
        replyTo={msg("kick")}
        onCancelReply={onCancel}
      />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "hi" } });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter", keyCode: 13 });
    await waitFor(() => expect(onCancel).toHaveBeenCalledWith("m1"));
  });

  it("labels the box as a reply, not a fan-out", () => {
    render(<ChatComposer statuses={statuses} limits={[]} configured replyTo={msg("kick")} />);
    expect(screen.getByRole("textbox").getAttribute("aria-label")).toBe("Reply to viewer");
  });
});
