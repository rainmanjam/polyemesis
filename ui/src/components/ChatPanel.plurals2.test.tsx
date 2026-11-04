// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ChatStatusList } from "./ChatPanel";
import { setLanguage } from "@/lib/i18n";
import type { ChatStatus } from "@/lib/types";

/* The received and sent counts under each platform in the chat status list.
 * Rendered in Spanish, where the participle agrees with the count. The number
 * sits in its own <span>, so the sentence is read from the parent line.
 * The catalogue has no plural rules, so count 1 has its own key: the line
 * used to read "1 recibidos · 1 enviados". */

afterEach(() => {
  cleanup();
  setLanguage("en");
});

const status = (received: number, sent: number): ChatStatus => ({
  platform: "twitch",
  state: "live",
  since: "2026-11-01T10:00:00Z",
  received,
  sent,
  restarts: 0,
  canSend: true,
});

/** The line holding the counts: the parent of the received number's span. */
function countsLine(received: number): string {
  const [num] = screen.getAllByText(String(received));
  return num.parentElement?.textContent ?? "";
}

describe("ChatStatusList: chatpage.receivedCount and chatpage.sentCount (es)", () => {
  it("says 1 recibido and 1 enviado for one of each", () => {
    setLanguage("es");
    render(<ChatStatusList statuses={[status(1, 1)]} />);
    expect(countsLine(1)).toBe("1 recibido · 1 enviado");
    expect(countsLine(1)).not.toContain("recibidos");
  });

  it("says 2 recibidos and 2 enviados for two of each", () => {
    setLanguage("es");
    render(<ChatStatusList statuses={[status(2, 2)]} />);
    expect(countsLine(2)).toBe("2 recibidos · 2 enviados");
  });
});
