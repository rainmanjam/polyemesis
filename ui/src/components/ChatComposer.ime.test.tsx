// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import type { ChatLimit, ChatStatus } from "@/lib/types";

/* A Japanese or Korean IME shows the candidate word while the user is still
 * composing it, and the Enter that CONFIRMS the candidate is a keydown with
 * isComposing set. The composer used to treat that Enter as "send": the
 * half-composed line went to every connected platform, and a chat message
 * cannot be taken back once it has been posted. Safari reports the same
 * confirming Enter with keyCode 229 and isComposing false, so both signals
 * have to be honoured. */

const sendChat = vi.fn();
vi.mock("@/lib/api", () => ({
  api: { sendChat: (...a: unknown[]) => sendChat(...a) },
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), message: vi.fn() },
}));

const { ChatComposer } = await import("./ChatPanel");

const statuses: ChatStatus[] = [
  {
    platform: "twitch",
    account: "twitch-acct",
    state: "live",
    since: "2026-01-01T09:00:00Z",
    received: 0,
    sent: 0,
    restarts: 0,
    canSend: true,
  },
];
const limits: ChatLimit[] = [];

beforeEach(() => {
  sendChat.mockReset();
  sendChat.mockResolvedValue({ results: [], sent: 0, failed: 0 });
});

afterEach(cleanup);

describe("the composer while an IME is confirming a word", () => {
  it("does not send when Enter confirms a composition (isComposing)", () => {
    render(<ChatComposer statuses={statuses} limits={limits} configured />);
    const box = screen.getByLabelText(/message/i) as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "にほんご" } });

    fireEvent.keyDown(box, { key: "Enter", code: "Enter", isComposing: true });

    expect(
      sendChat,
      "Enter that confirms an IME candidate sent a half-composed line",
    ).not.toHaveBeenCalled();
    expect(box.value, "the composed text was cleared by a send that should not happen").toBe(
      "にほんご",
    );
  });

  it("does not send on the keyCode 229 Enter that Safari reports during composition", () => {
    render(<ChatComposer statuses={statuses} limits={limits} configured />);
    const box = screen.getByLabelText(/message/i) as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "한국어" } });

    fireEvent.keyDown(box, { key: "Enter", code: "Enter", keyCode: 229 });

    expect(sendChat).not.toHaveBeenCalled();
  });

  it("still sends on a plain Enter outside composition", () => {
    render(<ChatComposer statuses={statuses} limits={limits} configured />);
    const box = screen.getByLabelText(/message/i) as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "hello" } });

    fireEvent.keyDown(box, { key: "Enter", code: "Enter", keyCode: 13 });

    expect(sendChat).toHaveBeenCalledWith("hello");
  });
});
