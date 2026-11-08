// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { resetRoleCache } from "./ChatUserTools";
import { api } from "@/lib/api";
import type { ChatPlatform, ChatUserCard as CardData } from "@/lib/types";

/* THE PLATFORM-SPECIFIC ROW ON THE VIEWER CARD.
 *
 * Twitch, X and YouTube have a row of tools; nothing else does. Role changes
 * ask a second time inline (never window.confirm), and a refusal is shown in
 * the server's own words. jsdom because Radix renders through a portal. */

const chatUser = vi.spyOn(api, "chatUser");
const twRead = vi.spyOn(api.twitchTool, "read");
const twPost = vi.spyOn(api.twitchTool, "post");
const twRemove = vi.spyOn(api.twitchTool, "remove");
const xList = vi.spyOn(api, "getChatModerators");
const xAdd = vi.spyOn(api, "addChatModerator");
const ytList = vi.spyOn(api, "youTubeChatModerators");
const ytAdd = vi.spyOn(api, "addYouTubeChatModerator");
const ytRemove = vi.spyOn(api, "removeYouTubeChatModerator");
const noteGet = vi.spyOn(api, "viewerNote");

const card = (platform: string): CardData =>
  ({ platform, authorId: "u1", name: "someone", messages: [], truncated: false, retentionNote: "" }) as CardData;

function draw(platform: ChatPlatform, channel = "streamer") {
  chatUser.mockResolvedValue(card(platform));
  return render(
    <ChatUserCard platform={platform} account="acct" authorId="u1" authorName="someone" channel={channel} open onOpenChange={() => {}} />,
  );
}

const btn = (name: RegExp | string) => screen.getByRole("button", { name }) as HTMLButtonElement;

describe("viewer card platform tools", () => {
  beforeEach(() => {
    for (const m of [chatUser, twRead, twPost, twRemove, xList, xAdd, ytList, ytAdd, ytRemove, noteGet]) m.mockReset();
    resetRoleCache();
    vi.spyOn(window, "confirm").mockImplementation(() => {
      throw new Error("window.confirm must not be used");
    });
    noteGet.mockResolvedValue({ platform: "twitch", authorId: "u1", note: "", tags: [] });
    twRead.mockImplementation(async (_r: string, _a: unknown, p?: Record<string, string>) =>
      ({ holders: p?.role === "vip" ? [{ userId: "u1", login: "s", name: "someone" }] : [] }) as never,
    );
  });
  afterEach(cleanup);

  it("Twitch: shows VIP status, asks again before removing, then removes by user id", async () => {
    twRemove.mockResolvedValue({} as never);
    draw("twitch");
    await waitFor(() => expect(screen.getByText("VIP: yes")).toBeTruthy());
    expect(screen.getByText("Moderator: no")).toBeTruthy();
    fireEvent.click(btn("Remove VIP"));
    expect(twRemove).not.toHaveBeenCalled();
    fireEvent.click(btn(/Remove VIP — confirm/));
    await waitFor(() => expect(twRemove).toHaveBeenCalledWith("roles", "acct", { role: "vip", userId: "u1" }));
  });

  it("Twitch: makes a moderator by user id after the second press", async () => {
    twPost.mockResolvedValue({} as never);
    draw("twitch");
    await waitFor(() => expect(screen.getByText("Moderator: no")).toBeTruthy());
    fireEvent.click(btn("Make moderator"));
    fireEvent.click(btn(/Make moderator — confirm/));
    await waitFor(() => expect(twPost).toHaveBeenCalledWith("roles", "acct", { role: "moderator", userId: "u1" }));
  });

  it("Twitch: a refusal is shown in the server's words and the card stays usable", async () => {
    twPost.mockRejectedValue(new Error("VIPs need the channel to be an Affiliate or Partner"));
    twRead.mockResolvedValue({ holders: [] } as never);
    draw("twitch");
    await waitFor(() => expect(screen.getByText("VIP: no")).toBeTruthy());
    fireEvent.click(btn("Make VIP"));
    fireEvent.click(btn(/Make VIP — confirm/));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Affiliate or Partner"));
    expect(btn("Make VIP").disabled).toBe(false);
  });

  it("Twitch: Warn needs a reason and sends it with the user id", async () => {
    twPost.mockResolvedValue({} as never);
    draw("twitch");
    await waitFor(() => expect(screen.getByText("VIP: yes")).toBeTruthy());
    expect(btn("Warn").disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Reason for the warning/), { target: { value: "  spoilers  " } });
    fireEvent.click(btn("Warn"));
    await waitFor(() => expect(twPost).toHaveBeenCalledWith("warnings", "acct", { userId: "u1", reason: "spoilers" }));
  });

  it("Twitch: offers the moderator card link only with a channel", async () => {
    draw("twitch");
    await waitFor(() => expect(screen.getByText("VIP: yes")).toBeTruthy());
    expect(screen.getByRole("button", { name: /Open moderator card on Twitch/ })).toBeTruthy();
    cleanup();
    chatUser.mockResolvedValue(card("twitch"));
    render(<ChatUserCard platform="twitch" account="acct" authorId="u1" authorName="someone" open onOpenChange={() => {}} />);
    await waitFor(() => expect(screen.getByText("VIP: yes")).toBeTruthy());
    expect(screen.queryByRole("button", { name: /Open moderator card on Twitch/ })).toBeNull();
  });

  it("X: lists status, makes a moderator after confirm, and says it covers every broadcast", async () => {
    xList.mockResolvedValue({ moderators: [], appliesToAllBroadcasts: true });
    xAdd.mockResolvedValue({ ids: ["u1"] });
    draw("x");
    await waitFor(() => expect(screen.getByText("Moderator: no")).toBeTruthy());
    expect(screen.getByText(/every broadcast this account hosts/)).toBeTruthy();
    fireEvent.click(btn("Make moderator"));
    expect(xAdd).not.toHaveBeenCalled();
    fireEvent.click(btn(/Make moderator — confirm/));
    await waitFor(() => expect(xAdd).toHaveBeenCalledWith("acct", "u1"));
  });

  it("YouTube: does not read the (50-unit) moderator list until a removal is confirmed, then removes by the listed id", async () => {
    ytList.mockResolvedValue({ moderators: [{ id: "mod-9", channelId: "u1" }] });
    ytRemove.mockResolvedValue({} as never);
    ytAdd.mockResolvedValue({} as never);
    draw("youtube");
    await waitFor(() => expect(screen.getByText(/50 YouTube quota units/)).toBeTruthy());
    expect(ytList).not.toHaveBeenCalled();
    fireEvent.click(btn("Remove moderator"));
    expect(ytList).not.toHaveBeenCalled();
    fireEvent.click(btn(/Remove moderator — confirm/));
    await waitFor(() => expect(ytRemove).toHaveBeenCalledWith({ platform: "youtube", account: "acct", id: "mod-9" }));
  });

  it("YouTube: a viewer who is not on the list is refused with a sentence, not a bad call", async () => {
    ytList.mockResolvedValue({ moderators: [{ id: "mod-1", channelId: "someone-else" }] });
    draw("youtube");
    await waitFor(() => expect(screen.getByText(/50 YouTube quota units/)).toBeTruthy());
    fireEvent.click(btn("Remove moderator"));
    fireEvent.click(btn(/Remove moderator — confirm/));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("not on this channel's moderator list"));
    expect(ytRemove).not.toHaveBeenCalled();
  });

  it("YouTube: makes a moderator by channel id", async () => {
    ytAdd.mockResolvedValue({} as never);
    draw("youtube");
    await waitFor(() => expect(screen.getByText(/50 YouTube quota units/)).toBeTruthy());
    fireEvent.click(btn("Make moderator"));
    fireEvent.click(btn(/Make moderator — confirm/));
    await waitFor(() => expect(ytAdd).toHaveBeenCalledWith({ platform: "youtube", account: "acct", channelId: "u1" }));
  });

  it("Other platforms show no tools row", async () => {
    draw("facebook");
    await waitFor(() => expect(chatUser).toHaveBeenCalled());
    expect(screen.queryByTestId("chat-user-tools")).toBeNull();
  });
});
