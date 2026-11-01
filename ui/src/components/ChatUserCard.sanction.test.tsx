// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import type { ChatSanction, ChatUserCard as CardData } from "@/lib/types";

/* UX COUNCIL A48: THE CARD SAYS WHAT IS IN FORCE, AND LIFT BAN FOLLOWS IT.
 *
 * Lift ban used to be offered for every viewer, banned or not, so pressing it
 * for someone who was never banned was a no-op reported as a success. The
 * server now says what polyemesis has in force; the button is disabled, with a
 * reason a screen reader is given, only when that answer is an explicit
 * "none". An absent answer is unknown and leaves it pressable.
 *
 * jsdom because Radix renders the dialog through a portal. */

const chatUser = vi.spyOn(api, "chatUser");

function draw(platform: "twitch" | "facebook" = "twitch") {
  return render(
    <ChatUserCard platform={platform} account="acct" authorId="u1" authorName="someone" open onOpenChange={() => {}} />,
  );
}

const card = (sanction?: ChatSanction): CardData =>
  ({ name: "someone", messages: [], truncated: false, retentionNote: "", sanction }) as CardData;

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

  it("disables Lift ban when nothing is in force, and says why to assistive tech", async () => {
    chatUser.mockResolvedValue(card({ action: "none" }));
    draw();
    await waitFor(() => expect(screen.getByText("No active ban")).toBeTruthy());
    expect(liftBan().disabled).toBe(true);
    const describedBy = liftBan().getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    const reason = document.getElementById(describedBy ?? "");
    expect(reason?.textContent).toMatch(/Nothing to lift/);
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

  it("gives the platform's own reason, not 'nothing to lift', where banning is impossible", async () => {
    chatUser.mockResolvedValue(card({ action: "none" }));
    draw("facebook");
    await waitFor(() => expect(screen.getByText("No active ban")).toBeTruthy());
    expect(liftBan().disabled).toBe(true);
    expect(screen.queryByText(/Nothing to lift/)).toBeNull();
  });
});
