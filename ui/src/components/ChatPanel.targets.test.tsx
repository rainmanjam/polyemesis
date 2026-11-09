// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import type { ChatLiveView, ChatPlatform, ChatStatus } from "@/lib/types";

/* The composer says where a message will go. By default that is the platforms
 * that are live, because a connected chat is not a streaming one: a test line
 * for one stream was posted in two channels that were not on air (staging,
 * 2026-11-09). The chips are the server's own reading (GET /chat/live), so a
 * chip that is on is exactly where a send with no target would go. */

const sendChat = vi.fn();
const chatLive = vi.fn();
const toastError = vi.fn();
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      sendChat: (...a: unknown[]) => sendChat(...a),
      chatLive: () => chatLive(),
    },
  };
});
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn(), message: vi.fn() },
}));

const { ChatComposer } = await import("./ChatPanel");
const { ApiError } = await import("@/lib/api");

function status(platform: ChatPlatform): ChatStatus {
  return {
    platform,
    account: `${platform}-acct`,
    state: "live",
    since: "2026-01-01T09:00:00Z",
    received: 0,
    sent: 0,
    restarts: 0,
    canSend: true,
  };
}
const statuses = [status("kick"), status("twitch"), status("youtube")];

function view(...livePlatforms: ChatPlatform[]): ChatLiveView {
  return {
    targets: statuses.map((s) => ({
      platform: s.platform,
      account: s.account,
      canSend: true,
      live: livePlatforms.includes(s.platform),
    })),
    anyLive: livePlatforms.length > 0,
  };
}

beforeEach(() => {
  sendChat.mockReset();
  sendChat.mockResolvedValue({
    results: [{ platform: "youtube", ok: true }],
    sent: 1,
    failed: 0,
    skipped: 0,
  });
  chatLive.mockReset();
  toastError.mockReset();
});
afterEach(cleanup);

async function renderComposer() {
  render(<ChatComposer statuses={statuses} limits={[]} configured />);
  // The reading arrives after the first paint.
  await screen.findByText("Sending to");
}
function type(text: string) {
  const box = screen.getByLabelText(/message/i) as HTMLTextAreaElement;
  fireEvent.change(box, { target: { value: text } });
  return box;
}
const chip = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}`, "i") });
const send = (box: HTMLElement) => fireEvent.keyDown(box, { key: "Enter", code: "Enter" });

describe("the composer's targets", () => {
  it("shows the live platforms as selected and the rest as not", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    expect(chip("YouTube").getAttribute("aria-pressed")).toBe("true");
    expect(chip("Kick").getAttribute("aria-pressed")).toBe("false");
    expect(chip("Twitch").getAttribute("aria-pressed")).toBe("false");
  });

  it("sends with no target while following live, so the server decides", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    send(type("hello"));
    await waitFor(() => expect(sendChat).toHaveBeenCalledWith("hello"));
  });

  it("names the platforms when the operator adds a non-live one", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    fireEvent.click(chip("Kick"));
    expect(chip("Kick").getAttribute("aria-pressed")).toBe("true");
    send(type("hello"));
    await waitFor(() =>
      expect(sendChat).toHaveBeenCalledWith("hello", { accounts: [{ platform: "kick", account: "kick-acct" }, { platform: "youtube", account: "youtube-acct" }] }),
    );
  });

  it("sends to all when every connected platform is chosen", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    fireEvent.click(screen.getByRole("button", { name: "All connected" }));
    send(type("hello"));
    await waitFor(() => expect(sendChat).toHaveBeenCalledWith("hello", { to: "all" }));
  });

  it("goes back to following live when the choice is undone", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    fireEvent.click(chip("Kick"));
    fireEvent.click(chip("Kick"));
    send(type("hello"));
    await waitFor(() => expect(sendChat).toHaveBeenCalledWith("hello"));
  });

  it("says nothing is live, offers the choice, and does not send until one is made", async () => {
    chatLive.mockResolvedValue(view());
    await renderComposer();
    expect(screen.getByRole("status").textContent).toContain("Nothing is live");
    const box = type("hello");
    send(box);
    expect(sendChat).not.toHaveBeenCalled();

    fireEvent.click(chip("Twitch"));
    expect(screen.queryByText(/Nothing is live/)).toBeNull();
    send(box);
    await waitFor(() => expect(sendChat).toHaveBeenCalledWith("hello", { accounts: [{ platform: "twitch", account: "twitch-acct" }] }));
  });

  it("falls back to the old behaviour when the live reading cannot be had", async () => {
    chatLive.mockRejectedValue(new Error("offline"));
    render(<ChatComposer statuses={statuses} limits={[]} configured />);
    send(type("hello"));
    await waitFor(() => expect(sendChat).toHaveBeenCalledWith("hello"));
    expect(screen.queryByText("Sending to")).toBeNull();
    // It does not claim to know where this goes: no "Send to <everyone>", and it says so.
    expect(await screen.findByText(/Can't tell what is live/)).toBeTruthy();
    expect((screen.getByLabelText(/message/i) as HTMLTextAreaElement).placeholder).toBe(
      "Send to live platforms",
    );
  });

  it("returns to the live default after a send, so an explicit choice is for one message", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    await renderComposer();
    fireEvent.click(screen.getByRole("button", { name: "All connected" }));
    send(type("one"));
    await waitFor(() => expect(sendChat).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(chip("Kick").getAttribute("aria-pressed")).toBe("false"));
  });
});

describe("the composer, one chip per account", () => {
  it("labels two channels on one platform and sends to the one chosen", async () => {
    const two = [
      { ...status("twitch"), account: "tw-a", channel: "alpha" },
      { ...status("twitch"), account: "tw-b", channel: "beta" },
    ];
    chatLive.mockResolvedValue({
      targets: [
        { platform: "twitch", account: "tw-a", canSend: true, live: true },
        { platform: "twitch", account: "tw-b", canSend: true, live: false },
      ],
      anyLive: true,
    });
    render(<ChatComposer statuses={two} limits={[]} configured />);
    await screen.findByText("Sending to");
    expect(chip("Twitch · alpha").getAttribute("aria-pressed")).toBe("true");
    expect(chip("Twitch · beta").getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(chip("Twitch · alpha")); // leave the live one out, take the other
    fireEvent.click(chip("Twitch · beta"));
    send(type("hello"));
    await waitFor(() =>
      expect(sendChat).toHaveBeenCalledWith("hello", {
        accounts: [{ platform: "twitch", account: "tw-b" }],
      }),
    );
  });
});

describe("a 409 because nothing is live", () => {
  it("is shown in the operator's language, not as the server's English", async () => {
    chatLive.mockResolvedValue(view("youtube"));
    sendChat.mockRejectedValue(
      new ApiError(409, `no platform is live; send "to":"all" or "platforms":[...] to choose`, "no_platform_live"),
    );
    await renderComposer();
    send(type("hello"));
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(toastError).toHaveBeenCalledWith("Nothing is live, so nothing was sent. Choose where to send it.");
  });
});
