// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatRules } from "./ChatRules";
import { api } from "@/lib/api";
import type { ChatSettings, ChatStatus } from "@/lib/types";

/* The card used to start every switch "off" whatever the room said, and an off
 * switch sent nothing -- so a mode set from Twitch's dashboard stayed on while
 * the card showed it removed (poka-yoke audit 2026-11-04). It now reads the
 * room first and, with the state known, Apply sends every rule explicitly. */

const statuses: ChatStatus[] = [{ platform: "twitch", account: "acct", state: "live" } as ChatStatus];

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const room: ChatSettings = {
  slowMode: true,
  slowModeSeconds: 30,
  followerMode: false,
  subscriberMode: true,
  emoteMode: false,
  uniqueChatMode: false,
  nonModeratorChatDelay: false,
};

const checked = (label: string) =>
  screen.getByLabelText(label).getAttribute("aria-checked") === "true";

describe("the chat rules card reads the room first", () => {
  it("shows the room's real modes as on", async () => {
    vi.spyOn(api, "getChatSettings").mockResolvedValue(room);
    render(<ChatRules statuses={statuses} />);
    await waitFor(() => expect(checked("Slow mode")).toBe(true));
    expect(checked("Subscribers only")).toBe(true);
    expect(checked("Followers only")).toBe(false);
  });

  it("keeps the switches disabled until the room has answered", () => {
    vi.spyOn(api, "getChatSettings").mockReturnValue(new Promise(() => {}));
    render(<ChatRules statuses={statuses} />);
    expect(screen.getByLabelText("Slow mode")).toHaveProperty("disabled", true);
  });

  it("turns a live mode off when its switch is switched off and applied", async () => {
    vi.spyOn(api, "getChatSettings").mockResolvedValue(room);
    const update = vi.spyOn(api, "updateChatSettings").mockResolvedValue({ status: "ok" });
    render(<ChatRules statuses={statuses} />);
    await waitFor(() => expect(checked("Slow mode")).toBe(true));
    fireEvent.click(screen.getByLabelText("Slow mode"));
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    const sent = update.mock.calls[0][2];
    expect(sent.slowMode).toBe(false);
    expect(sent.subscriberMode).toBe(true);
    expect(sent.emoteMode).toBe(false);
  });

  it("starts over for a different account instead of showing the previous room", async () => {
    const read = vi.spyOn(api, "getChatSettings").mockResolvedValueOnce(room);
    const { rerender } = render(<ChatRules statuses={statuses} />);
    await waitFor(() => expect(checked("Slow mode")).toBe(true));
    read.mockReturnValueOnce(new Promise(() => {})); // the new room has not answered
    rerender(<ChatRules statuses={[{ ...statuses[0], account: "other-acct" }]} />);
    expect(screen.getByLabelText("Slow mode")).toHaveProperty("disabled", true);
    expect(checked("Slow mode")).toBe(false);
    expect(read).toHaveBeenLastCalledWith("twitch", "other-acct");
  });
});
