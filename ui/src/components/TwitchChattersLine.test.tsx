// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { TwitchChattersLine } from "./AccountLiveStats";

/* The chatter count sits beside the viewer count and must never be able to hurt
 * it: it is asked once, on demand, and any failure -- an old-scope refusal above
 * all -- removes the line instead of showing an error. Driven through the real
 * request path (a stubbed fetch), so what is under test is the line's reaction
 * to what the server sends. */

function stubFetch(answer: () => Response) {
  const urls: string[] = [];
  vi.stubGlobal("fetch", async (url: string) => {
    urls.push(url);
    return answer();
  });
  return urls;
}
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("TwitchChattersLine", () => {
  it("shows Twitch's count, asked once, and again only on request", async () => {
    let total = 41;
    const urls = stubFetch(() => json(200, { total }));
    render(<TwitchChattersLine accountRef="acct" />);
    await waitFor(() => expect(screen.getByText("Connected to chat: 41")).toBeTruthy());
    expect(urls).toEqual(["/api/v1/chat/twitch/chatters?account=acct"]);

    total = 44;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(screen.getByText("Connected to chat: 44")).toBeTruthy());
    expect(urls.length).toBe(2);
  });

  it("renders nothing when Twitch refuses, rather than an error", async () => {
    const urls = stubFetch(() => json(400, { error: "reconnect Twitch" }));
    const { container } = render(<TwitchChattersLine accountRef="acct" />);
    await waitFor(() => expect(urls.length).toBe(1));
    // Let the refusal settle before judging what is on screen.
    await new Promise((r) => setTimeout(r, 20));
    expect(container.textContent).toBe("");
  });
});
