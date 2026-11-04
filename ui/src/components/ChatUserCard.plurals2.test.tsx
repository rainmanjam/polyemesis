// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ChatUserCard } from "./ChatUserCard";
import { api } from "@/lib/api";
import { setLanguage } from "@/lib/i18n";
import type { ChatUserCard as CardData } from "@/lib/types";

/* The error toast after a hide-all that partly fails. The total is the number
 * of lines the card was holding; with no plural rules a card holding one line
 * reported "Hid 0 of 1 lines". */

const chatUser = vi.spyOn(api, "chatUser");
const hide = vi.spyOn(api, "hideChatMessage");

const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn(), message: vi.fn() },
}));

const lines = (n: number): CardData =>
  ({
    name: "someone",
    messages: Array.from({ length: n }, (_, i) => ({
      id: `m${i + 1}`,
      text: `line ${i + 1}`,
      at: "2026-08-20T12:00:00Z",
      platform: "facebook" as const,
      author: { id: "u1", name: "someone" },
    })),
  }) as CardData;

const button = (name: RegExp) => screen.getByRole("button", { name }) as HTMLButtonElement;

/** Holds `total` lines, the last of which the server refuses to hide. */
async function hideAllWithOneFailure(total: number) {
  chatUser.mockResolvedValue(lines(total));
  hide.mockImplementation(async (args) => {
    if (args.id === `m${total}`) throw new Error("upstream refused");
    return {} as Awaited<ReturnType<typeof api.hideChatMessage>>;
  });
  render(<ChatUserCard platform="facebook" authorId="u1" authorName="someone" open onOpenChange={() => {}} />);
  await waitFor(() => expect(button(/Hide here only/).disabled).toBe(false));
  fireEvent.click(button(/Hide here only/));
  await waitFor(() => expect(toastError).toHaveBeenCalled());
}

beforeEach(() => {
  chatUser.mockReset();
  hide.mockReset();
  toastError.mockReset();
});
afterEach(() => {
  cleanup();
  setLanguage("en");
});

describe("ChatUserCard: chatCard.hidePartial (en)", () => {
  it("says 0 of 1 line, not 0 of 1 lines, when the card's only line fails", async () => {
    await hideAllWithOneFailure(1);
    expect(toastError).toHaveBeenCalledWith("Hid 0 of 1 line; 1 failed. Check the card before retrying.");
    expect(toastError).not.toHaveBeenCalledWith("Hid 0 of 1 lines; 1 failed. Check the card before retrying.");
  });

  it("says 1 of 2 lines when one of two lines fails", async () => {
    await hideAllWithOneFailure(2);
    expect(toastError).toHaveBeenCalledWith("Hid 1 of 2 lines; 1 failed. Check the card before retrying.");
  });
});
