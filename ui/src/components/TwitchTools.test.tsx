// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";

import { TwitchTools } from "./TwitchTools";
import { api } from "@/lib/api";
import type { ChatStatus } from "@/lib/types";

/* The Twitch channel tools panel: what an operator can reach, and what Twitch is
 * then asked for. Written against the calls made (api.twitchTool.*), because the
 * names of the controls are the thing that was already there in the last audit
 * of this kind and let an unreachable feature through.
 *
 * AutoMod's PUT replaces, so the case that matters most is that "set each
 * category" sends overallLevel null and every category, and the simple form
 * sends the overall level alone. */

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const read = vi.spyOn(api.twitchTool, "read");
const post = vi.spyOn(api.twitchTool, "post");
const put = vi.spyOn(api.twitchTool, "put");
const patch = vi.spyOn(api.twitchTool, "patch");
const remove = vi.spyOn(api.twitchTool, "remove");
const unban = vi.spyOn(api, "unbanChatUser");

const statuses: ChatStatus[] = [{ platform: "twitch", account: "acct", state: "live" } as ChatStatus];

const AUTO = {
  overallLevel: 2, aggression: 0, bullying: 0, disability: 0, misogyny: 0,
  raceEthnicityOrReligion: 0, sexBasedTerms: 0, sexualitySexOrGender: 0, swearing: 0,
};

beforeEach(() => {
  for (const m of [read, post, put, patch, remove, unban]) m.mockReset();
  post.mockResolvedValue({});
  put.mockResolvedValue({});
  patch.mockResolvedValue({});
  remove.mockResolvedValue({});
  unban.mockResolvedValue({ status: "unbanned" } as never);
  read.mockImplementation((async (resource: string, _a: unknown, params?: Record<string, string>) => {
    switch (resource) {
      case "pin": return { pin: null };
      case "shield": return { active: false };
      case "automod-settings": return AUTO;
      case "blocked-terms": return { terms: [{ id: "t1", text: "badword" }] };
      case "roles": return { holders: [{ userId: params?.role === "vip" ? "v1" : "m1", login: "x", name: params?.role === "vip" ? "Vee" : "Em" }] };
      case "banned": return { banned: [{ userId: "b1", login: "b", name: "Bee", reason: "spam" }] };
      case "unban-requests": return { requests: [{ id: "u1", userId: "b1", login: "b", name: "Bee", text: "sorry", status: "pending" }] };
      case "chatters": return params?.list ? { total: 3, chatters: [{ userId: "1", login: "a", name: "Ann" }], truncated: true } : { total: 3 };
      default: throw new Error("unexpected " + resource);
    }
  }) as never);
});
afterEach(cleanup);

describe("TwitchTools", () => {
  it("renders nothing without a Twitch account", () => {
    const { container } = render(<TwitchTools statuses={[{ platform: "kick", account: "k" } as ChatStatus]} />);
    expect(container.textContent).toBe("");
  });

  it("says it is untried on a live account", () => {
    render(<TwitchTools statuses={statuses} />);
    expect(screen.getByText(/not yet tried on a live account/)).toBeTruthy();
  });

  it("sends an announcement with its colour, and a shoutout, a warning and a pin", async () => {
    render(<TwitchTools statuses={statuses} />);
    await waitFor(() => expect(read).toHaveBeenCalledWith("pin", "acct"));

    fireEvent.change(screen.getByLabelText("Announcement"), { target: { value: "Hello" } });
    fireEvent.change(screen.getByLabelText("Highlight colour"), { target: { value: "purple" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Send" })[0]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("announcements", "acct", { message: "Hello", color: "purple" }));

    post.mockResolvedValueOnce({ nextAnyAt: "2030-01-01T00:02:00Z", targets: {} });
    fireEvent.change(screen.getByLabelText("Shoutout"), { target: { value: "friend" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Send" })[1]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("shoutouts", "acct", { login: "friend" }));
    await waitFor(() => expect(screen.getByText(/Next shoutout possible at/)).toBeTruthy());

    fireEvent.change(screen.getAllByLabelText("Viewer name")[0], { target: { value: "rude" } });
    fireEvent.change(screen.getByLabelText("Reason shown to them"), { target: { value: "stop" } });
    fireEvent.click(screen.getByRole("button", { name: "Warn a viewer" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("warnings", "acct", { login: "rude", reason: "stop" }));

    fireEvent.change(screen.getByLabelText("Message id"), { target: { value: "m9" } });
    fireEvent.change(screen.getByLabelText("Seconds (blank: until the stream ends)"), { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Pin" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("pin", "acct", { messageId: "m9", durationSeconds: 120 }));
    fireEvent.click(screen.getByRole("button", { name: "Change time" }));
    await waitFor(() => expect(patch).toHaveBeenCalledWith("pin", "acct", { messageId: "m9", durationSeconds: 120 }));
    fireEvent.click(screen.getByRole("button", { name: "Unpin" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("pin", "acct", { messageId: "m9" }));
  });

  it("shows Twitch's refusal as it arrives", async () => {
    post.mockRejectedValue(new Error("Twitch refused. Reconnect it."));
    render(<TwitchTools statuses={statuses} />);
    fireEvent.change(screen.getByLabelText("Announcement"), { target: { value: "x" } });
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Send" })[0]).toHaveProperty("disabled", false));
    fireEvent.click(screen.getAllByRole("button", { name: "Send" })[0]);
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Twitch refused. Reconnect it."));
  });

  it("switches Shield Mode and sends AutoMod's overall level alone", async () => {
    render(<TwitchTools statuses={statuses} />);
    fireEvent.click(screen.getByRole("tab", { name: "Safety" }));
    await waitFor(() => expect(screen.getByText("Shield Mode is off")).toBeTruthy());
    put.mockResolvedValueOnce({ active: true });
    fireEvent.click(screen.getByRole("switch", { name: /Shield Mode is off/ }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("shield", "acct", { active: true }));

    await waitFor(() => expect(screen.getByLabelText("Overall level")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Overall level"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith("automod-settings", "acct", { settings: { overallLevel: 4 } }),
    );
  });

  it("sends every AutoMod category, with overallLevel null, when set one by one", async () => {
    render(<TwitchTools statuses={statuses} />);
    fireEvent.click(screen.getByRole("tab", { name: "Safety" }));
    await waitFor(() => expect(screen.getByLabelText("Set each category")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Set each category"));
    const swearing = screen.getByText("Swearing").querySelector("select")!;
    fireEvent.change(swearing, { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [, , body] = put.mock.calls.find((c) => c[0] === "automod-settings")!;
    const s = (body as { settings: Record<string, unknown> }).settings;
    expect(s.overallLevel).toBeNull();
    expect(s.swearing).toBe(3);
    expect(s.bullying).toBe(0);
  });

  it("decides a held message by id, manages blocked terms and flags a viewer", async () => {
    render(<TwitchTools statuses={statuses} />);
    fireEvent.click(screen.getByRole("tab", { name: "Safety" }));
    await waitFor(() => expect(screen.getByText("badword")).toBeTruthy());

    fireEvent.change(screen.getByLabelText("Held message"), { target: { value: "h1" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Deny" })[0]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("held-messages", "acct", { messageId: "h1", allow: false }));

    fireEvent.change(screen.getByLabelText("Blocked terms"), { target: { value: "newterm" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("blocked-terms", "acct", { text: "newterm" }));

    post.mockResolvedValueOnce({ added: ["a"], present: [], skipped: [] });
    fireEvent.click(screen.getByRole("button", { name: "Add plain automod words to Twitch" }));
    await waitFor(() => expect(screen.getByText("1 added, 0 already there, 0 skipped")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("blocked-terms", "acct", { id: "t1" }));

    fireEvent.change(screen.getByLabelText("Suspicious viewer"), { target: { value: "sus" } });
    fireEvent.click(screen.getByRole("button", { name: "Flag" }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith("suspicious", "acct", { login: "sus", status: "ACTIVE_MONITORING" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Clear flag" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("suspicious", "acct", { login: "sus" }));
  });

  it("lists people, and acts on roles, bans, appeals and the chatter list", async () => {
    render(<TwitchTools statuses={statuses} />);
    fireEvent.click(screen.getByRole("tab", { name: "People" }));
    await waitFor(() => expect(screen.getByText("Em")).toBeTruthy());
    expect(screen.getByText("Vee")).toBeTruthy();
    expect(screen.getByText(/Connected to chat: 3/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Remove Em" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("roles", "acct", { role: "moderator", userId: "m1" }));

    fireEvent.change(screen.getByLabelText("VIPs"), { target: { value: "newvip" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[1]);
    await waitFor(() => expect(post).toHaveBeenCalledWith("roles", "acct", { role: "vip", login: "newvip" }));

    fireEvent.click(screen.getByRole("button", { name: "Unban" }));
    await waitFor(() => expect(unban).toHaveBeenCalledWith({ platform: "twitch", account: "acct", userId: "b1" }));

    fireEvent.click(screen.getAllByRole("button", { name: "Approve" })[0]);
    await waitFor(() => expect(patch).toHaveBeenCalledWith("unban-requests", "acct", { id: "u1", status: "approved" }));

    fireEvent.click(screen.getByRole("button", { name: "Show names" }));
    await waitFor(() => expect(screen.getByText(/Ann/)).toBeTruthy());
  });
});
