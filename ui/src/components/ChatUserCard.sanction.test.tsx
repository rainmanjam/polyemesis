// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import type { ChatSanction, ChatUserCard as CardData } from "@/lib/types";

/* UX COUNCIL A48: THE CARD SAYS WHAT POLYEMESIS HAS IN FORCE.
 *
 * The server reports the ban or timeout polyemesis placed. That record is NOT
 * authoritative -- a ban made on the platform's own dashboard never reaches
 * it -- so the owner's rule is that neither "none" nor an absent answer
 * disables Lift ban. Only a platform that cannot ban at all does.
 *
 * jsdom because Radix renders the dialog through a portal. */

const chatUser = vi.spyOn(api, "chatUser");

function draw(platform: "twitch" | "facebook" = "twitch") {
  return render(
    <ChatUserCard platform={platform} account="acct" authorId="u1" authorName="someone" open onOpenChange={() => {}} />,
  );
}

const card = (sanction?: ChatSanction): CardData =>
  ({ platform: "twitch", authorId: "u1", name: "someone", messages: [], truncated: false, retentionNote: "", sanction }) as CardData;

const liftBan = () => screen.getByRole("button", { name: /Lift ban/ }) as HTMLButtonElement;

describe("A48: the user card's sanction state", () => {
  beforeEach(() => chatUser.mockReset());
  afterEach(cleanup);

  it("asks for the card in the channel it was opened from", async () => {
    chatUser.mockResolvedValue(card({ action: "none" }));
    draw();
    await waitFor(() => expect(chatUser).toHaveBeenCalled());
    expect(chatUser.mock.calls[0][0]).toMatchObject({ platform: "twitch", account: "acct", authorId: "u1" });
  });

  // A recorded "none" is not "no ban anywhere": a ban from the platform's own
  // dashboard is invisible to polyemesis, and lifting it from here must work.
  it("keeps Lift ban enabled when polyemesis recorded no sanction, and says only that", async () => {
    chatUser.mockResolvedValue(card({ action: "none" }));
    draw();
    await waitFor(() => expect(screen.getByText("No ban placed through polyemesis")).toBeTruthy());
    expect(liftBan().disabled).toBe(false);
    expect(screen.queryByText(/No active ban/)).toBeNull();
  });

  it("keeps Lift ban available for a timeout and shows when it ends", async () => {
    chatUser.mockResolvedValue(
      card({ action: "timeout", until: "2026-11-01T18:10:00Z", since: "2026-11-01T18:00:00Z", by: "automod" }),
    );
    draw();
    await waitFor(() => expect(screen.getByText("Timed out")).toBeTruthy());
    expect(liftBan().disabled).toBe(false);
    expect(liftBan().getAttribute("aria-describedby")).toBeNull();
    expect(screen.getByText(/Ends at .*Placed by automod/)).toBeTruthy();
  });

  it("keeps Lift ban available for a permanent ban", async () => {
    chatUser.mockResolvedValue(card({ action: "ban", since: "2026-11-01T18:00:00Z", by: "operator" }));
    draw();
    await waitFor(() => expect(screen.getByText("Banned")).toBeTruthy());
    expect(liftBan().disabled).toBe(false);
    expect(screen.getByText(/Permanent\. Placed at .* by an operator/)).toBeTruthy();
  });

  // THE unknown-versus-none rule. A server that could not read the record must
  // not take the one working control away from a moderator with a banned viewer.
  it("treats an absent sanction as unknown and leaves Lift ban pressable", async () => {
    chatUser.mockResolvedValue(card(undefined));
    draw();
    await waitFor(() => expect(screen.getByText("Ban state unknown")).toBeTruthy());
    expect(liftBan().disabled).toBe(false);
    expect(screen.getByText(/could not tell whether this person is banned/)).toBeTruthy();
  });

  // The one certain case: the platform cannot ban, so there is nothing to lift.
  it("disables Lift ban only where the platform cannot ban, with the platform's reason", async () => {
    chatUser.mockResolvedValue(card({ action: "none" }));
    draw("facebook");
    await waitFor(() => expect(screen.getByText("No ban placed through polyemesis")).toBeTruthy());
    expect(liftBan().disabled).toBe(true);
    expect(liftBan().title).toMatch(/Facebook/);
  });

  // review #2: a time on another day carries its date.
  it("dates a timeout that ends on another day", async () => {
    const tomorrow = new Date(Date.now() + 26 * 3600_000).toISOString();
    chatUser.mockResolvedValue(card({ action: "timeout", until: tomorrow, by: "operator" }));
    draw();
    await waitFor(() => expect(screen.getByText("Timed out")).toBeTruthy());
    const detail = screen.getByText(/Ends at/).textContent ?? "";
    expect(detail).toMatch(/\d{1,4}[./-]\d{1,2}[./-]\d{1,4}/);
  });

  // review #8: "Lift ban stays available" is not said where it is disabled.
  it("does not promise Lift ban on a platform that cannot ban", async () => {
    chatUser.mockResolvedValue(card(undefined));
    draw("facebook");
    await waitFor(() => expect(screen.getByText("Ban state unknown")).toBeTruthy());
    expect(screen.getByText(/could not tell whether this person is banned/)).toBeTruthy();
    expect(screen.queryByText(/stays available/)).toBeNull();
  });
});

